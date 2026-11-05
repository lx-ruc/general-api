package gateway

import (
	"fmt"
	"testing"
	"time"

	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
)

func mustExec(t *testing.T, f *dbFixture, sql string, args ...any) {
	t.Helper()
	if err := f.db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// seedChannel 建厂商 1 + 模型 m1（归属厂商 1）+ 渠道挂厂商 1 + 能力；keys 为厂商池明文 Key
// （同厂商多渠道共享同一池——与生产行为一致）
func seedChannel(t *testing.T, f *dbFixture, id int64, name string, poolKeys []string, priority int) {
	t.Helper()
	now := time.Now().Unix()
	mustExec(t, f, `INSERT INTO vendors (id, name, status, created_at, updated_at)
		SELECT 1, 'v1', 1, ?, ? WHERE NOT EXISTS (SELECT 1 FROM vendors WHERE id = 1)`, now, now)
	mustExec(t, f, `INSERT INTO models (name, vendor_id, status, created_at, updated_at)
		SELECT 'm1', 1, 1, ?, ? WHERE NOT EXISTS (SELECT 1 FROM models WHERE name = 'm1')`, now, now)
	mustExec(t, f, `INSERT INTO channels (id,name,vendor_id,base_url,path,weight,priority,status,created_at,updated_at)
		VALUES (?,?,1, 'http://up.test', '/v1/chat/completions', 1, ?, 1, ?, ?)`, id, name, priority, now, now)
	for i, k := range poolKeys {
		mustExec(t, f, `INSERT INTO vendor_keys (vendor_id,key_enc,weight,status,created_at,updated_at)
			VALUES (1,?,1,1,?,?)`, k, now+int64(i), now+int64(i))
	}
	mustExec(t, f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, 'm1')`, id)
}

func TestSelectCandidatesPoolExpansion(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", []string{"k1", "k2", "k3"}, 10)
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem()

	cands, _, err := SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil || len(cands) != 3 {
		t.Fatalf("应展开 3 个候选，got %d err=%v", len(cands), err)
	}
	seen := map[int64]bool{}
	for _, c := range cands {
		if c.ChannelID != 1 || c.KeyID == 0 {
			t.Fatalf("池内候选应有非零 KeyID: %+v", c)
		}
		if c.KeyScope() != fmt.Sprintf("ck:1:%d", c.KeyID) {
			t.Fatalf("scope 应为厂商维度 ck:1:{keyID}，got %s", c.KeyScope())
		}
		seen[c.KeyID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("候选应为 3 把不同 key，got %v", seen)
	}
}

// 同厂商两个端点共用一套池：候选 = 端点 × Key 的笛卡尔积，Key 冷却对全部端点生效
func TestSelectCandidatesSharedPoolAcrossChannels(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ep1", []string{"k1", "k2"}, 10)
	seedChannel(t, f, 2, "ep2", nil, 10) // 第二个端点不另带 Key（共用厂商池）
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem()
	cd.SetCooldown("ck:1:2", time.Minute) // 冷却一把

	cands, _, err := SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 { // k1 × {ep1, ep2}
		t.Fatalf("共用池应展开 2 个候选（1 把可用 Key × 2 端点），got %d", len(cands))
	}
	chans := map[int64]bool{}
	for _, c := range cands {
		if c.KeyID == 2 {
			t.Fatal("冷却中的 key 不应出现在候选里")
		}
		chans[c.ChannelID] = true
	}
	if len(chans) != 2 {
		t.Fatalf("两个端点都应承接，got %v", chans)
	}
}

func TestSelectCandidatesAllKeysCoolingSkipsChannels(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", []string{"k1", "k2"}, 10)
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem()
	cd.SetCooldown("ck:1:1", time.Minute)
	cd.SetCooldown("ck:1:2", time.Minute) // 厂商池全部 key 冷却

	cands, stats, err := SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 || stats.CoolingKeys != 2 {
		t.Fatalf("全池冷却应无候选且 CoolingKeys=2，got cands=%d stats=%+v", len(cands), stats)
	}
}

func TestSelectCandidatesDisabledKeyExcluded(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", []string{"k1", "k2"}, 10)
	mustExec(t, f, "UPDATE vendor_keys SET status = 0 WHERE vendor_id = 1 AND key_enc = 'k1'")
	cipher, _ := crypto.NewCipher("")

	cands, _, err := SelectCandidates(f.db, cipher, "m1", coord.NewMem())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].UpstreamKey != "k2" {
		t.Fatalf("禁用 key 应在 SQL 层排除，got %+v", cands)
	}
}

func TestSelectCandidatesPriorityGroups(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "high", []string{"hk"}, 10)
	seedChannel(t, f, 2, "low", []string{"lk"}, 1)
	cipher, _ := crypto.NewCipher("")

	cands, _, err := SelectCandidates(f.db, cipher, "m1", coord.NewMem())
	if err != nil {
		t.Fatal(err)
	}
	// 共享池：2 把 Key × 2 个端点 = 4 个候选；高优先级端点（priority 10）组排前面
	if len(cands) != 4 {
		t.Fatalf("共享池应展开 4 个候选（2 Key × 2 端点），got %d", len(cands))
	}
	if cands[0].Priority < cands[2].Priority {
		t.Fatal("高优先级组的候选应排在前面（降级备用）")
	}
}

// 模型归属与渠道厂商不一致时（多厂商模型未拆分的防御路径）：该厂商渠道不参与路由
func TestSelectCandidatesVendorMismatchFiltered(t *testing.T) {
	f := newTestDB(t)
	now := time.Now().Unix()
	seedChannel(t, f, 1, "v1-ep", []string{"k1"}, 10) // 厂商 1 承接 m1
	// 厂商 2 也挂了 m1 能力，但 m1 归属厂商 1 → 渠道应被过滤
	mustExec(t, f, `INSERT INTO vendors (id, name, status, created_at, updated_at) VALUES (2, 'v2', 1, ?, ?)`, now, now)
	mustExec(t, f, `INSERT INTO channels (id,name,vendor_id,base_url,path,weight,priority,status,created_at,updated_at)
		VALUES (2,'v2-ep',2,'http://up.test','/v1/chat/completions',1,99,1,?,?)`, now, now)
	mustExec(t, f, `INSERT INTO vendor_keys (vendor_id,key_enc,weight,status,created_at,updated_at)
		VALUES (2,'k2',1,1,?,?)`, now, now)
	mustExec(t, f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (2, 'm1')`)
	cipher, _ := crypto.NewCipher("")

	cands, _, err := SelectCandidates(f.db, cipher, "m1", coord.NewMem())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].ChannelID != 1 {
		t.Fatalf("非归属厂商的渠道应被过滤，got %+v", cands)
	}
}

// DecryptFailed 计数（厂商维度）：全部解密失败计满、正确 key 归零；
// 全冷却时不计解密失败（冷却可恢复，整体保持 429 语义）
func TestSelectCandidatesDecryptFailedStats(t *testing.T) {
	f := newTestDB(t)
	store, _ := crypto.NewCipher(crypto.RandomKeyB64()) // 存库用的 key
	wrong, _ := crypto.NewCipher(crypto.RandomKeyB64()) // 轮换后的 key
	seedChannel(t, f, 1, "ch1", []string{"p1-enc", "p2-enc"}, 10)
	// 覆写为不可解密文（seedChannel 存的是明文）
	mustExec(t, f, `UPDATE vendor_keys SET key_enc = ? WHERE vendor_id = 1 AND key_enc = 'p1-enc'`, mustEnc(t, store, "p1"))
	mustExec(t, f, `UPDATE vendor_keys SET key_enc = ? WHERE vendor_id = 1 AND key_enc = 'p2-enc'`, mustEnc(t, store, "p2"))

	// 错误 key：候选空，该厂商计解密失败
	cands, stats, err := SelectCandidates(f.db, wrong, "m1", coord.NewMem())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 || stats.Channels != 1 || stats.KeyedVendors != 1 || stats.DecryptFailed != 1 || stats.CoolingKeys != 0 {
		t.Fatalf("全解密失败统计不符: cands=%d stats=%+v", len(cands), stats)
	}

	// 正确 key：2 候选，解密失败归零
	cands, stats, err = SelectCandidates(f.db, store, "m1", coord.NewMem())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || stats.DecryptFailed != 0 {
		t.Fatalf("正确 key 应 2 候选且零解密失败: cands=%d stats=%+v", len(cands), stats)
	}

	// 全冷却 + 错误 key：冷却优先（可恢复），不计解密失败
	cd := coord.NewMem()
	cd.SetCooldown("ck:1:1", time.Minute)
	cd.SetCooldown("ck:1:2", time.Minute)
	cands, stats, err = SelectCandidates(f.db, wrong, "m1", cd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 || stats.DecryptFailed != 0 || stats.KeyedVendors != 1 || stats.CoolingKeys != 2 {
		t.Fatalf("全冷却场景统计不符: cands=%d stats=%+v", len(cands), stats)
	}
}

func mustEnc(t *testing.T, c *crypto.Cipher, s string) string {
	t.Helper()
	enc, err := c.Encrypt(s)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}
