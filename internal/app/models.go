package app

import "time"

type Account struct {
	ID               uint       `json:"id" gorm:"primaryKey"`
	Name             string     `json:"name"`
	Kind             string     `json:"kind"`
	BaseURL          string     `json:"base_url"`
	Enabled          bool       `json:"enabled"`
	UseProxy         bool       `json:"use_proxy" gorm:"not null;default:false"`
	AuthMode         string     `json:"auth_mode"`
	Username         string     `json:"username"`
	UserID           string     `json:"user_id"`
	Token            string     `json:"-"`
	RefreshToken     string     `json:"-" gorm:"not null;default:''"`
	Password         string     `json:"-"`
	Cookie           string     `json:"-"`
	LoginExtra       string     `json:"-" gorm:"not null;default:''"`
	Currency         string     `json:"currency"`
	QuotaPerUnit     float64    `json:"quota_per_unit"`
	LowBalance       float64    `json:"low_balance"`
	BlockEmpty       bool       `json:"block_empty"`
	IntervalSeconds  int        `json:"interval_seconds"`
	Balance          *float64   `json:"balance"`
	Used             *float64   `json:"used"`
	TodayUsed        *float64   `json:"today_used"`
	TodayDate        string     `json:"today_date"`
	TodayCollectedAt *time.Time `json:"today_collected_at"`
	CollectionState  string     `json:"collection_state" gorm:"not null;default:''"`
	Status           string     `json:"status"`
	LastError        string     `json:"last_error"`
	Rates            string     `json:"rates"`
	Subscriptions    string     `json:"subscriptions"`
	Announcements    string     `json:"announcements"`
	Warnings         string     `json:"warnings"`
	CheckedAt        *time.Time `json:"checked_at"`
	NextCheckAt      time.Time  `json:"next_check_at"`
	CreatedAt        time.Time  `json:"created_at"`
	Revision         int        `json:"-"`
}

type Snapshot struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	AccountID uint      `json:"account_id" gorm:"index"`
	Balance   *float64  `json:"balance"`
	Used      *float64  `json:"used"`
	TodayUsed *float64  `json:"today_used"`
	TodayDate string    `json:"today_date"`
	Status    string    `json:"status"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

type Channel struct {
	ID                 uint       `json:"id" gorm:"primaryKey"`
	Name               string     `json:"name"`
	AccountID          *uint      `json:"account_id" gorm:"index;uniqueIndex:idx_channel_upstream_key"`
	UpstreamKeyID      *int64     `json:"upstream_key_id" gorm:"uniqueIndex:idx_channel_upstream_key"`
	Account            *Account   `json:"-" gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	BaseURL            string     `json:"base_url"`
	Protocol           string     `json:"protocol"`
	Protocols          []string   `json:"protocols" gorm:"serializer:json;type:text"`
	Enabled            bool       `json:"enabled"`
	UseProxy           bool       `json:"use_proxy" gorm:"not null;default:false"`
	AutoDisabledReason string     `json:"auto_disabled_reason" gorm:"not null;default:''"`
	APIKey             string     `json:"-"`
	Models             string     `json:"models"`
	DiscoveredModels   string     `json:"discovered_models" gorm:"not null;default:''"`
	ModelsBaseline     string     `json:"models_baseline" gorm:"not null;default:''"`
	ModelsCheckedAt    *time.Time `json:"models_checked_at"`
	ModelsSyncError    string     `json:"models_sync_error" gorm:"not null;default:''"`
	TimeoutSeconds     int        `json:"timeout_seconds"`
	CooldownSeconds    int        `json:"cooldown_seconds"`
	CreatedAt          time.Time  `json:"created_at"`
}

type Group struct {
	ID                uint              `json:"id" gorm:"primaryKey"`
	Name              string            `json:"name" gorm:"uniqueIndex"`
	Vendor            string            `json:"vendor" gorm:"not null;default:''"`
	Strategy          string            `json:"strategy"`
	ActiveRouteID     uint              `json:"active_route_id" gorm:"not null;default:0"`
	RelayConfig       *GroupRelayConfig `json:"relay_config" gorm:"serializer:json;type:text"`
	InputPriceMicros  int64             `json:"input_price_micros"`
	OutputPriceMicros int64             `json:"output_price_micros"`
	PriceMode         string            `json:"price_mode" gorm:"not null;default:''"`
	PriceModel        string            `json:"price_model" gorm:"not null;default:''"`
	PriceSource       string            `json:"price_source" gorm:"not null;default:''"`
	PriceSourceModel  string            `json:"price_source_model" gorm:"not null;default:''"`
	PriceSyncError    string            `json:"price_sync_error" gorm:"not null;default:''"`
	PriceUpdatedAt    *time.Time        `json:"price_updated_at"`
	Pricing           *modelPricing     `json:"pricing" gorm:"serializer:json;type:text"`
	Routes            []Route           `json:"routes" gorm:"foreignKey:GroupID"`
}

type Route struct {
	ID        uint          `json:"id" gorm:"primaryKey"`
	GroupID   uint          `json:"group_id" gorm:"index"`
	ChannelID uint          `json:"channel_id"`
	Channel   *Channel      `json:"-" gorm:"foreignKey:ChannelID;constraint:OnDelete:RESTRICT"`
	Model     string        `json:"model"`
	Weight    int           `json:"weight"`
	Priority  int           `json:"priority"`
	Pricing   *modelPricing `json:"pricing" gorm:"serializer:json;type:text"`
}

type APIKey struct {
	ID                  uint       `json:"id" gorm:"primaryKey"`
	Name                string     `json:"name"`
	Prefix              string     `json:"prefix"`
	Digest              string     `json:"-" gorm:"uniqueIndex"`
	TokenCipher         string     `json:"-"`
	CanCopy             bool       `json:"can_copy" gorm:"-"`
	Enabled             bool       `json:"enabled"`
	ExpiresAt           *time.Time `json:"expires_at"`
	AllowedModels       []string   `json:"allowed_models" gorm:"serializer:json;type:text"`
	BudgetMicros        *int64     `json:"budget_micros"`
	DailyBudgetMicros   *int64     `json:"daily_budget_micros"`
	MonthlyBudgetMicros *int64     `json:"monthly_budget_micros"`
	MaxConcurrent       int        `json:"max_concurrent" gorm:"not null;default:0"`
	RequestsPerMinute   int        `json:"requests_per_minute" gorm:"not null;default:0"`
	DailyUsedMicros     int64      `json:"daily_used_micros" gorm:"-"`
	MonthlyUsedMicros   int64      `json:"monthly_used_micros" gorm:"-"`
	DailyResetAt        *time.Time `json:"daily_reset_at" gorm:"-"`
	MonthlyResetAt      *time.Time `json:"monthly_reset_at" gorm:"-"`
	ActiveRequests      int        `json:"active_requests" gorm:"-"`
	UsedMicros          int64      `json:"used_micros" gorm:"not null;default:0"`
	CreatedAt           time.Time  `json:"created_at"`
}

type Session struct {
	Digest    string `gorm:"primaryKey"`
	ExpiresAt time.Time
}

type RequestLog struct {
	SummaryOnly        bool               `json:"summary_only,omitempty" gorm:"-"`
	ID                 uint               `json:"id" gorm:"primaryKey"`
	MetricsVersion     int                `json:"metrics_version" gorm:"not null;default:0"`
	RequestID          string             `json:"request_id" gorm:"index"`
	KeyID              uint               `json:"key_id"`
	ChannelID          uint               `json:"channel_id"`
	AccountName        string             `json:"account_name" gorm:"not null;default:''"`
	Model              string             `json:"model"`
	Protocol           string             `json:"protocol"`
	ReasoningEffort    string             `json:"reasoning_effort" gorm:"not null;default:''"`
	ReasoningTokens    *int64             `json:"reasoning_tokens"`
	UsageRecorded      bool               `json:"usage_recorded" gorm:"not null;default:false"`
	CacheReadRecorded  bool               `json:"cache_read_recorded" gorm:"not null;default:false"`
	CacheWriteRecorded bool               `json:"cache_write_recorded" gorm:"not null;default:false"`
	UsageWarning       string             `json:"usage_warning" gorm:"not null;default:''"`
	Stream             bool               `json:"stream"`
	Status             int                `json:"status"`
	Attempts           int                `json:"attempts"`
	InputTokens        int64              `json:"input_tokens"`
	OutputTokens       int64              `json:"output_tokens"`
	CacheReadTokens    int64              `json:"cache_read_tokens" gorm:"not null;default:0"`
	CacheWriteTokens   int64              `json:"cache_write_tokens" gorm:"not null;default:0"`
	CacheWrite1hTokens int64              `json:"cache_write_1h_tokens" gorm:"not null;default:0"`
	CostMicros         int64              `json:"cost_micros"`
	CostStatus         string             `json:"cost_status" gorm:"not null;default:''"`
	CostWarning        string             `json:"cost_warning" gorm:"not null;default:''"`
	CostSource         string             `json:"cost_source" gorm:"not null;default:''"`
	DurationMS         int64              `json:"duration_ms"`
	StartedAt          *time.Time         `json:"started_at"`
	FirstTokenMS       *int64             `json:"first_token_ms"`
	FirstContentMS     *int64             `json:"first_content_ms"`
	GenerationMS       *int64             `json:"generation_ms"`
	CostDetail         *requestCostDetail `json:"cost_detail" gorm:"serializer:json;type:text"`
	Error              string             `json:"error"`
	CreatedAt          time.Time          `json:"created_at" gorm:"index"`
	State              string             `json:"state" gorm:"not null;default:'';index"`
	ChannelName        string             `json:"channel_name" gorm:"not null;default:''"`
	KeyName            string             `json:"key_name" gorm:"not null;default:''"`
	UpstreamModel      string             `json:"upstream_model" gorm:"not null;default:''"`
	UpstreamProtocol   string             `json:"upstream_protocol" gorm:"not null;default:''"`
	FirstByteMS        *int64             `json:"first_byte_ms"`
	FinishedAt         *time.Time         `json:"finished_at"`
	RoutingReason      string             `json:"routing_reason" gorm:"-"`
	RetryAt            *time.Time         `json:"retry_at" gorm:"-"`
	FinishReason       string             `json:"finish_reason" gorm:"-"`
	IncompleteReason   string             `json:"incomplete_reason" gorm:"-"`
}

type RequestDetail struct {
	RequestLogID uint        `gorm:"primaryKey"`
	Log          *RequestLog `gorm:"foreignKey:RequestLogID;constraint:OnDelete:CASCADE"`
	Payload      string      `json:"-"`
}

type RequestDay struct {
	Date                string `json:"date" gorm:"primaryKey;size:10"`
	Requests            int64  `json:"requests"`
	Errors              int64  `json:"errors"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheWriteTokens    int64  `json:"cache_write_tokens"`
	CacheWrite1hTokens  int64  `json:"cache_write_1h_tokens"`
	CostMicros          int64  `json:"cost_micros"`
	CostUnknownRequests int64  `json:"cost_unknown_requests"`
}

type RequestHour struct {
	Hour                    int64  `gorm:"primaryKey;autoIncrement:false"`
	ChannelID               uint   `gorm:"primaryKey;autoIncrement:false"`
	Model                   string `gorm:"primaryKey"`
	ChannelName             string
	Requests                int64
	Errors                  int64
	Successes               int64
	InputTokens             int64
	OutputTokens            int64
	CostMicros              int64
	CostUnknownRequests     int64
	DurationMS              int64
	FirstTokenMS            int64
	FirstTokenCount         int64
	SemanticFirstTokenMS    int64 `gorm:"not null;default:0"`
	SemanticFirstTokenCount int64 `gorm:"not null;default:0"`
	GenerationMS            int64
	GenerationTokens        int64
	ObservedRequests        int64 `gorm:"not null;default:0"`
	FirstAttemptSuccesses   int64 `gorm:"not null;default:0"`
	RetriedSuccesses        int64 `gorm:"not null;default:0"`
	FailedRequests          int64 `gorm:"not null;default:0"`
	AttemptOverheadMS       int64 `gorm:"not null;default:0"`
	LastRequestUnix         int64 `json:"last_request_unix"`
}

type Alert struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	AccountID     uint      `json:"account_id"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	Detail        string    `json:"detail"`
	Delivery      string    `json:"delivery"`
	DeliveryError string    `json:"delivery_error"`
	Attempts      int       `json:"attempts"`
	NextRetryAt   time.Time `json:"next_retry_at"`
	CreatedAt     time.Time `json:"created_at" gorm:"index"`
}

type AlertState struct {
	ID          uint   `gorm:"primaryKey"`
	AccountID   uint   `gorm:"uniqueIndex:account_kind"`
	Kind        string `gorm:"uniqueIndex:account_kind"`
	LastEmitted time.Time
}

type NotificationChannel struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	Name         string    `json:"name" gorm:"uniqueIndex"`
	Type         string    `json:"type"`
	Enabled      bool      `json:"enabled"`
	UseProxy     bool      `json:"use_proxy" gorm:"not null;default:false"`
	URL          string    `json:"-"`
	Secret       string    `json:"-"`
	Token        string    `json:"-"`
	ChatID       string    `json:"chat_id"`
	SendKey      string    `json:"-"`
	SMTPHost     string    `json:"smtp_host"`
	SMTPPort     int       `json:"smtp_port"`
	SMTPTLS      string    `json:"smtp_tls"`
	SMTPUsername string    `json:"smtp_username"`
	SMTPPassword string    `json:"-"`
	SMTPFrom     string    `json:"smtp_from"`
	SMTPTo       []string  `json:"smtp_to" gorm:"serializer:json;type:text"`
	Events       []string  `json:"events" gorm:"serializer:json;type:text"`
	Accounts     []uint    `json:"accounts" gorm:"serializer:json;type:text"`
	Retries      int       `json:"retries"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type NotificationDelivery struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	AlertID     *uint      `json:"alert_id" gorm:"uniqueIndex:idx_notification_recipient"`
	Alert       *Alert     `json:"-" gorm:"foreignKey:AlertID;constraint:OnDelete:CASCADE"`
	ChannelID   uint       `json:"channel_id" gorm:"uniqueIndex:idx_notification_recipient;index"`
	ChannelName string     `json:"channel_name"`
	ChannelType string     `json:"channel_type"`
	Title       string     `json:"title"`
	Status      string     `json:"status" gorm:"index"`
	Error       string     `json:"error"`
	Attempts    int        `json:"attempts"`
	NextRetryAt time.Time  `json:"next_retry_at"`
	SentAt      *time.Time `json:"sent_at"`
	CreatedAt   time.Time  `json:"created_at" gorm:"index"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Setting struct {
	RuntimeOptions              `gorm:"embedded"`
	ID                          uint   `gorm:"primaryKey"`
	ProxyEnabled                bool   `json:"-" gorm:"not null;default:false"`
	ProxyURL                    string `json:"-" gorm:"not null;default:''"`
	NotificationsMigrated       bool   `json:"-" gorm:"not null;default:false"`
	WebhookURL                  string `json:"webhook_url"`
	WebhookSecret               string `json:"-"`
	TelegramToken               string `json:"-" gorm:"not null;default:''"`
	TelegramChatID              string `json:"telegram_chat_id" gorm:"not null;default:''"`
	AlertCooldownMinutes        int    `json:"alert_cooldown_minutes"`
	RetentionDays               int    `json:"retention_days"`
	RequestSummaryRetentionDays int    `json:"request_summary_retention_days" gorm:"not null;default:365"`
	RequestHistoryMigrated      bool   `json:"-" gorm:"not null;default:false"`
}
