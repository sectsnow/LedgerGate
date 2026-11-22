package app

import "gorm.io/gorm"

func enabledChannels(db *gorm.DB) *gorm.DB {
	// 账户暂停只阻断使用，保留渠道自己的启停设置供恢复监控后继续使用。
	accounts := db.Session(&gorm.Session{NewDB: true}).Model(&Account{}).Select("id").Where("enabled = ?", true)
	return db.Where("channels.enabled = ?", true).Where("channels.account_id IS NULL OR channels.account_id IN (?)", accounts)
}
