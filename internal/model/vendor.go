package model

// Vendor 厂商实体：Key 池 / 渠道(端点) / 模型 / 授权 / 定价的归属主体。
// status=0 主开关：该厂商全部端点视为不可路由
type Vendor struct {
	ID          int64  `gorm:"primaryKey" json:"id"`
	Name        string `json:"name"`        // 厂商标识（如 volces、volces/Thu）
	DisplayName string `json:"display_name"` // 展示名（空=用 name）
	Status      int    `json:"status"`
	Remark      string `json:"remark"`
	CreatedAt   int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt   int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Vendor) TableName() string { return "vendors" }

// VendorKey 厂商 Key 池：厂商下全部端点共用（轮询/冷却/401 自动禁用同旧 channel_keys 机制）
type VendorKey struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	VendorID  int64  `json:"vendor_id"`
	KeyEnc    string `json:"-"` // AES-GCM b64 密文（未启用加密则明文）
	Weight    int    `json:"weight"`
	Status    int    `json:"status"`
	Remark    string `json:"remark"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (VendorKey) TableName() string { return "vendor_keys" }

// OrgVendorGrant 客户级厂商开通：授厂商 ⟺ 组内全部模型可用（新模型自动继承）
type OrgVendorGrant struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	OrgID     int64  `json:"org_id"`
	VendorID  int64  `json:"vendor_id"`
	GrantedBy *int64 `json:"granted_by"`
	Source    string `json:"source"` // grant=审批/手动 backfill=存量折算
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (OrgVendorGrant) TableName() string { return "org_vendor_grants" }

// UserVendorGrant 子账号级厂商授权（数据面放行主口径）
type UserVendorGrant struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	UserID    int64  `json:"user_id"`
	VendorID  int64  `json:"vendor_id"`
	GrantedBy *int64 `json:"granted_by"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (UserVendorGrant) TableName() string { return "user_vendor_grants" }

// OrgVendorPrice 客户×厂商一套三价（组内模型统一生效；模型级覆盖优先）
type OrgVendorPrice struct {
	ID                  int64  `gorm:"primaryKey" json:"id"`
	OrgID               int64  `json:"org_id"`
	VendorID            int64  `json:"vendor_id"`
	InputPrice          int64  `json:"input_price"`
	OutputPrice         int64  `json:"output_price"`
	InputCacheHitPrice  int64  `json:"input_cache_hit_price"` // 0=同输入价
	Remark              string `json:"remark"`
	CreatedAt           int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt           int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (OrgVendorPrice) TableName() string { return "org_vendor_prices" }
