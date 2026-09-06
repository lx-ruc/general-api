package model

// Model 对外暴露的模型与定价；name 即客户端请求里的 model 参数
type Model struct {
	ID          int64  `gorm:"primaryKey" json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Vendor      string `json:"vendor"`
	InputPrice  int64  `json:"input_price"`  // 售卖价 / 1M prompt tokens（客户扣减）
	OutputPrice int64  `json:"output_price"` // 售卖价 / 1M completion tokens
	CostInputPrice  int64 `json:"cost_input_price"`  // 厂商成本价（毛利核算）
	CostOutputPrice int64 `json:"cost_output_price"`
	Status      int    `json:"status"`
	Remark      string `json:"remark"`
	CreatedAt   int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt   int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Model) TableName() string { return "models" }
