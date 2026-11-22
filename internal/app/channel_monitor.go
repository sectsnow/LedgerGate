package app

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func autoDisableUnavailableChannels(tx *gorm.DB, previous, account Account, current rateSnapshot) ([]uint, error) {
	if previous.Status == "error" || strings.Contains(previous.Warnings, "倍率采集失败") {
		return nil, nil
	}
	old := readRateSnapshot(previous.Rates)
	if old.Groups == nil || current.Groups == nil {
		return nil, nil
	}
	previousGroups, currentGroups := map[string]bool{}, map[string]bool{}
	for _, group := range old.Groups {
		previousGroups[group.GroupID] = true
	}
	for _, group := range current.Groups {
		currentGroups[group.GroupID] = true
	}
	var channels []Channel
	if err := tx.Where("account_id = ? AND enabled = ?", account.ID, true).Order("id").Find(&channels).Error; err != nil {
		return nil, err
	}
	var disabled []uint
	var details []string
	for _, channel := range channels {
		rate, matched := current.Channels[channel.ID]
		before, previouslyMatched := old.Channels[channel.ID]
		identity := channelRateIdentity(channel)
		// 只在连续两次完整采集确认相同密钥的具体分组缺失时停用，倍率缺失本身不能证明渠道不可用。
		if !matched || !previouslyMatched || rate.Identity != identity || before.Identity != identity || rate.GroupID != before.GroupID {
			continue
		}
		if rate.GroupID == "" || rate.GroupID == "0" || rate.GroupID == "auto" || rate.Multiplier != nil || before.Multiplier != nil || currentGroups[rate.GroupID] || previousGroups[rate.GroupID] {
			continue
		}
		reason := fmt.Sprintf("连续两次采集未找到可用上游分组“%s”，且上游未提供倍率", rate.Group)
		result := tx.Model(&channel).Where("enabled = ?", true).Updates(map[string]any{"enabled": false, "auto_disabled_reason": reason})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected > 0 {
			disabled = append(disabled, channel.ID)
			details = append(details, fmt.Sprintf("渠道“%s”：%s", channel.Name, reason))
		}
	}
	if len(disabled) > 0 {
		if err := emitAlert(tx, account, "channel_auto_disabled", "上游分组不可用，渠道已自动停用", strings.Join(details, "\n")); err != nil {
			return nil, err
		}
	}
	return disabled, nil
}
