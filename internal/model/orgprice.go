package model

// OrgModelPrice 客户级差异化定价：同一模型对不同客户可设不同售卖价。
// 单位与 models 一致（点/1M token）；input_cache_hit_price=0 表示同输入价。
// 覆盖行不存在时走 models 默认价（数据面 relay 在加载模型后做点查覆盖）。
type OrgModelPrice struct {
	ID                  int64  `gorm:"primaryKey" json:"id"`
	OrgID               int64  `json:"org_id"`
	ModelName           string `json:"model_name"`
	InputPrice          int64  `json:"input_price"`
	OutputPrice         int64  `json:"output_price"`
	InputCacheHitPrice  int64  `json:"input_cache_hit_price"`
	Remark              string `json:"remark"`
	CreatedAt           int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt           int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (OrgModelPrice) TableName() string { return "org_model_prices" }
