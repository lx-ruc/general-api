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

// seedChannel 建渠道+能力；keys 为池内明文 key（空 = legacy 单 key 模式）
func seedChannel(t *testing.T, f *dbFixture, id int64, name, key string, poolKeys []string, priority int) {
	t.Helper()
	now := time.Now().Unix()
	if len(poolKeys) > 0 {
		mustExec(t, f, `INSERT INTO channels (id,name,base_url,path,weight,priority,status,created_at,updated_at)
			VALUES (?,?, 'http://up.test', '/v1/chat/completions', 1, ?, 1, ?, ?)`, id, name, priority, now, now)
		for i, k := range poolKeys {
			mustExec(t, f, `INSERT INTO channel_keys (channel_id,key_enc,weight,status,created_at,updated_at)
				VALUES (?,?,1,1,?,?)`, id, k, now+int64(i), now+int64(i))
		}
	} else {
		mustExec(t, f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
			VALUES (?,?, 'http://up.test', '/v1/chat/completions', ?, 1, ?, 1, ?, ?)`, id, name, key, priority, now, now)
	}
	mustExec(t, f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, 'm1')`, id)
}

func TestSelectCandidatesPoolExpansion(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", "", []string{"k1", "k2", "k3"}, 10)
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem(0)

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
			t.Fatalf("scope 格式错误: %s", c.KeyScope())
		}
		seen[c.KeyID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("候选应为 3 把不同 key，got %v", seen)
	}
}

func TestSelectCandidatesCooldownFilter(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", "", []string{"k1", "k2", "k3"}, 10)
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem(0)
	cd.SetCooldown("ck:1:2", time.Minute) // 冷却中间那把

	cands, _, err := SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("冷却 key 应被过滤，期望 2 个候选 got %d", len(cands))
	}
	for _, c := range cands {
		if c.KeyID == 2 {
			t.Fatal("冷却中的 key 不应出现在候选里")
		}
	}
}

func TestSelectCandidatesAllKeysCoolingSkipsChannel(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", "", []string{"k1"}, 10)
	seedChannel(t, f, 2, "ch2", "", []string{"k2"}, 1)
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem(0)
	cd.SetCooldown("ck:1:1", time.Minute) // 渠道 1 全部 key 冷却

	cands, _, err := SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].ChannelID != 2 {
		t.Fatalf("渠道 1 应被整体跳过，got %+v", cands)
	}
}

func TestSelectCandidatesLegacyFallbackAndScope(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", "legacy-key", nil, 10)
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem(0)

	cands, _, err := SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil || len(cands) != 1 {
		t.Fatalf("legacy 应回退单 key，got %d err=%v", len(cands), err)
	}
	if cands[0].KeyID != 0 || cands[0].UpstreamKey != "legacy-key" {
		t.Fatalf("legacy 候选字段错误: %+v", cands[0])
	}
	if cands[0].KeyScope() != "ck:1:legacy" {
		t.Fatalf("legacy scope 应为 ck:1:legacy, got %s", cands[0].KeyScope())
	}

	// legacy 冷却 → 渠道跳过
	cd.SetCooldown("ck:1:legacy", time.Minute)
	cands, _, err = SelectCandidates(f.db, cipher, "m1", cd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("legacy 冷却后应无候选，got %d", len(cands))
	}
}

func TestSelectCandidatesDisabledKeyExcluded(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "ch1", "", []string{"k1", "k2"}, 10)
	mustExec(t, f, "UPDATE channel_keys SET status = 0 WHERE channel_id = 1 AND key_enc = 'k1'")
	cipher, _ := crypto.NewCipher("")

	cands, _, err := SelectCandidates(f.db, cipher, "m1", coord.NewMem(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].UpstreamKey != "k2" {
		t.Fatalf("禁用 key 应在 SQL 层排除，got %+v", cands)
	}
}

func TestSelectCandidatesPriorityGroups(t *testing.T) {
	f := newTestDB(t)
	seedChannel(t, f, 1, "high", "", []string{"hk"}, 10)
	seedChannel(t, f, 2, "low", "", []string{"lk"}, 1)
	cipher, _ := crypto.NewCipher("")

	cands, _, err := SelectCandidates(f.db, cipher, "m1", coord.NewMem(0))
	if err != nil || len(cands) != 2 {
		t.Fatalf("应有两个候选 got %d err=%v", len(cands), err)
	}
	if cands[0].Priority < cands[1].Priority {
		t.Fatal("高优先级组的候选应排在前面（降级备用）")
	}
}
