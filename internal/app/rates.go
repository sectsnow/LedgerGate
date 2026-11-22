package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
)

type upstreamRate struct {
	GroupID        string   `json:"group_id"`
	Group          string   `json:"group"`
	Multiplier     *float64 `json:"multiplier"`
	BaseMultiplier *float64 `json:"base_multiplier"`
}

type rateSnapshot struct {
	Groups   []upstreamRate       `json:"groups"`
	Channels map[uint]channelRate `json:"channels"`
}

type channelRate struct {
	upstreamRate
	Identity string `json:"identity"`
}

func channelRateIdentity(channel Channel) string {
	return digest(channel.BaseURL + "\x00" + channel.APIKey)
}

func rateNumber(value any) *float64 {
	rate, ok := number(value)
	if !ok || rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return nil
	}
	return &rate
}

func readRateSnapshot(raw string) rateSnapshot {
	var snapshot rateSnapshot
	_ = json.Unmarshal([]byte(raw), &snapshot)
	return snapshot
}

func (a *App) fetchUpstreamGroups(ctx context.Context, account *Account) (map[string]upstreamRate, object, error) {
	groups := map[string]upstreamRate{}
	custom := object{}
	path := "/api/user/self/groups"
	if account.Kind == "sub2api" {
		path = "/api/v1/groups/available"
	}
	value, _, err := a.accountRequest(ctx, account, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, err
	}
	if account.Kind == "sub2api" {
		items, ok := value.([]any)
		if !ok {
			return nil, nil, errors.New("上游分组列表格式无效")
		}
		value, _, err = a.accountRequest(ctx, account, http.MethodGet, "/api/v1/groups/rates", nil)
		if err != nil {
			return nil, nil, err
		}
		var valid bool
		custom, valid = value.(map[string]any)
		if !valid {
			return nil, nil, errors.New("上游用户倍率格式无效")
		}
		for _, item := range items {
			group := obj(item)
			id := integer(group["id"])
			if id <= 0 {
				return nil, nil, errors.New("上游分组缺少有效 ID")
			}
			key := strconv.FormatInt(id, 10)
			base := rateNumber(group["rate_multiplier"])
			rate := upstreamRate{GroupID: key, Group: str(group["name"]), BaseMultiplier: base, Multiplier: base}
			if rate.Group == "" {
				rate.Group = "分组 #" + key
			}
			if override, exists := custom[key]; exists {
				rate.Multiplier = rateNumber(override)
			}
			groups[key] = rate
		}
	} else {
		items, ok := value.(map[string]any)
		if !ok {
			return nil, nil, errors.New("上游分组倍率格式无效")
		}
		for id, item := range items {
			data := obj(item)
			groups[id] = upstreamRate{GroupID: id, Group: id, BaseMultiplier: rateNumber(data["base_ratio"]), Multiplier: rateNumber(data["ratio"])}
		}
	}
	return groups, custom, nil
}

func (a *App) fetchAccountRates(ctx context.Context, account *Account) (rateSnapshot, error) {
	groups, custom, err := a.fetchUpstreamGroups(ctx, account)
	if err != nil {
		return rateSnapshot{}, err
	}
	snapshot := rateSnapshot{Groups: []upstreamRate{}, Channels: map[uint]channelRate{}}
	for _, group := range groups {
		snapshot.Groups = append(snapshot.Groups, group)
	}
	sort.Slice(snapshot.Groups, func(i, j int) bool { return snapshot.Groups[i].GroupID < snapshot.Groups[j].GroupID })
	var channels []Channel
	if err := a.DB.WithContext(ctx).Where("account_id = ?", account.ID).Find(&channels).Error; err != nil {
		return rateSnapshot{}, errors.New("读取关联渠道失败")
	}
	keyRates := map[int64]upstreamRate{}
	secretRates := map[string]upstreamRate{}
	if len(channels) > 0 {
		seen := 0
		for page := 1; ; page++ {
			if page > 100 {
				return rateSnapshot{}, errors.New("上游密钥超过本次采集页数限制")
			}
			items, total, err := a.requestAccountKeys(ctx, account, page, 100)
			if err != nil {
				return rateSnapshot{}, err
			}
			for _, item := range items {
				key := obj(item)
				groupID, groupName := str(key["group"]), str(key["group"])
				if account.Kind == "sub2api" {
					group := obj(key["group"])
					groupID = strconv.FormatInt(integer(key["group_id"]), 10)
					groupName = str(group["name"])
					if _, exists := groups[groupID]; !exists {
						base := rateNumber(group["rate_multiplier"])
						rate := upstreamRate{GroupID: groupID, Group: groupName, BaseMultiplier: base, Multiplier: base}
						if override, exists := custom[groupID]; exists {
							rate.Multiplier = rateNumber(override)
						}
						groups[groupID] = rate
					}
				}
				rate, exists := groups[groupID]
				if !exists {
					rate = upstreamRate{GroupID: groupID, Group: groupName}
					groups[groupID] = rate
				}
				if rate.Group == "" {
					rate.Group = "分组 #" + groupID
					groups[groupID] = rate
				}
				if id := integer(key["id"]); id > 0 {
					keyRates[id] = rate
				}
				if secret := str(key["key"]); completeUpstreamKey(secret) {
					secretRates[upstreamKeyDigest(account.Kind, secret)] = rate
				}
			}
			seen += len(items)
			if seen >= total {
				break
			}
			if len(items) == 0 {
				return rateSnapshot{}, errors.New("上游密钥列表不完整")
			}
		}
	}
	// 手动添加的同一把密钥也沿用导入渠道的分组，不按渠道名称猜测倍率。
	for _, channel := range channels {
		if channel.BaseURL != account.BaseURL || channel.UpstreamKeyID == nil {
			continue
		}
		if rate, ok := keyRates[*channel.UpstreamKeyID]; ok {
			secret, err := a.unseal(channel.APIKey)
			if err != nil {
				return rateSnapshot{}, err
			}
			if secret != "" {
				secretRates[upstreamKeyDigest(account.Kind, secret)] = rate
			}
		}
	}
	for _, channel := range channels {
		if channel.BaseURL != account.BaseURL {
			continue
		}
		secret, err := a.unseal(channel.APIKey)
		if err != nil {
			return rateSnapshot{}, err
		}
		if rate, ok := secretRates[upstreamKeyDigest(account.Kind, secret)]; ok && secret != "" {
			snapshot.Channels[channel.ID] = channelRate{upstreamRate: rate, Identity: channelRateIdentity(channel)}
		}
	}
	return snapshot, nil
}
