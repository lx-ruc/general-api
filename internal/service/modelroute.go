package service

// 模型「当前可路由」的统一口径：启用模型 × 存在启用渠道 × 该渠道有可用上游 Key。
//
// 背景：渠道 Key 因上游 401 被自动禁用后，模型与渠道能力仍处启用态，各处
// 「可用模型」目录继续宣传死模型，客户端调通列表后调用却 503
// (channel_key_missing)。可见性必须联动 Key 健康，但用派生查询而非改写
// models.status——同一模型可由多渠道承接，仅凭单一渠道 Key 失效就翻转模型
// 状态会误伤存活路由，且 Key 修复后无法自愈。
//
// 语义与 gateway.SelectCandidates 的 KeyedChannels 判定保持一致：
// Key 池内有启用行（channel_keys.status=1），或 legacy 单 Key 密文非空；
// 并同样要求 base_url/path 非空（SelectCandidates 会静默剔除空值渠道）。
// 谓词引用模型别名为 m 的查询（各调用点均以 m 别名扫描 models 表）。
const ModelHasLiveUpstream = `EXISTS (
	SELECT 1 FROM channel_abilities ra
	JOIN channels rc ON rc.id = ra.channel_id
	WHERE ra.model_name = m.name
	  AND rc.status = 1 AND rc.base_url <> '' AND rc.path <> ''
	  AND (rc.upstream_key_enc <> '' OR EXISTS (
	      SELECT 1 FROM channel_keys rk WHERE rk.channel_id = rc.id AND rk.status = 1)))`
