#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""计费海量对账回归：大批量请求 × 大 token 数 × 分阶段调价 / 客户覆盖价，逐行核账。

与 run.py 的分工：run.py 测链路结构（小固定 usage），本脚本测「算账不能有差」——
约 4,000 万 token 流量过网关，中途调模型价、加客户覆盖价、追充额度，随后：
  R1  每行 cost == ceil((in×in价 + out×out价)/1M)（按行内价格快照列复算，128 位口径）
  R2  价格快照列与时间线一致（A 段 P1 / B 段调价 P2 / C 段客户覆盖价 O1 / 跨客户默认价）
  R3  每行 vendor_cost == 同公式换成本价
  R4  users/orgs.quota_used == 各自 Σ行 cost（双记账，精确相等）
  R5  脚本侧独立期望 Σ == 实际 Σ（缺行/重复结算/错行都会暴露）
  R6  Σquota_grants == quota_limit（含中途追充）
  R7  账单勾稽：期初快照 + 期内授权 − 消耗 == 期末快照；chain_ok 由服务端判
  R8  厂商账单对账：diff==0 / 篡改后 diff 精确
  R9  no_usage（上游漏 usage）不计费、statement 披露笔数
  R10 精确缓存命中 cost=0；被拒尝试（403/404/400）零成本不进账单明细
  R11 余额预警：越 80% 线 → 客户管理员站内信
  R12 超扣封顶在单请求成本内，且已扣数 == 精确期望

前置：网关 :8081（admin/admin123456；database.path 指向仓库 data/token_.db；
gateway.cache_ttl > 0）。自带 mock 上游 :9103（按请求序号给预设 token 数）。
"""
import json
import os
import sqlite3
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from datetime import datetime, timedelta

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GW = 'http://127.0.0.1:8081'
MOCK_PORT = 9103
DB_PATH = os.path.join(REPO, 'data', 'token_.db')  # 锚到仓库根，与执行时 cwd 无关
TS = str(int(time.time()))

results = []


def check(name, cond, detail=''):
    tag = 'PASS' if cond else 'FAIL'
    results.append((tag, name, detail))
    print(f'{tag}  {name}' + (f'  — {detail}' if detail and not cond else ''), flush=True)


def call(method, path, token=None, body=None, key=None, raw=None, ctype='application/json'):
    req = urllib.request.Request(GW + path, method=method)
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    if key:
        req.add_header('Authorization', 'Bearer ' + key)
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    if data is not None:
        req.add_header('Content-Type', ctype)
    try:
        with urllib.request.urlopen(req, data, timeout=30) as resp:
            payload = resp.read()
            try:
                return resp.status, dict(resp.headers), json.loads(payload or b'{}')
            except json.JSONDecodeError:
                return resp.status, dict(resp.headers), payload.decode(errors='replace')
    except urllib.error.HTTPError as e:
        payload = e.read()
        try:
            return e.code, dict(e.headers), json.loads(payload or b'{}')
        except json.JSONDecodeError:
            return e.code, dict(e.headers), payload.decode(errors='replace')


def login(username, password):
    for attempt in range(6):
        st, _, r = call('POST', '/api/auth/login', body={'username': username, 'password': password})
        if st == 200 and r.get('token'):
            return r['token']
        if st == 429 and attempt < 5:  # 登录限流 5/分/IP：脚本登录点多，退避等窗
            time.sleep(13)
            continue
        raise AssertionError((username, st, r))
    raise AssertionError((username, st, r))


def db_rows(sql, args=()):
    conn = sqlite3.connect(f'file:{DB_PATH}?mode=ro', uri=True, timeout=10)
    try:
        return conn.execute(sql, args).fetchall()
    finally:
        conn.close()


def ceil_m(n):
    """与 Go CalcCost 同口径：ceil(n / 1M)，全整数。"""
    return -(-n // 1_000_000)


# ---------------- mock 上游：按请求序号回预设 usage ----------------

# token 计划（in, out）：大数 + 非整除值，专打 ceil 边界与乘法精度
SCHEDULE = [
    (12345, 5432), (67890, 98765), (234567, 123456), (99999, 77777), (87654, 32109),
    (111111, 22222), (345678, 90123), (56789, 4321), (999999, 888888), (13579, 24680),
]
SPECIAL = {i: (100_000, 40_000) for i in range(300, 304)}  # 预警/超扣专用（520,000 点/笔，序号独立防缓存命中）
NO_USAGE_IDX = set(range(210, 215))          # mock 不回 usage → no_usage 不计费


def usage_for(idx):
    if idx in NO_USAGE_IDX:
        return None
    if idx in SPECIAL:
        return SPECIAL[idx]
    return SCHEDULE[idx % len(SCHEDULE)]


class MockHandler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        length = int(self.headers.get('Content-Length', 0))
        body = json.loads(self.rfile.read(length) or b'{}')
        idx = 0
        try:
            content = body['messages'][0]['content']
            idx = int(content.rsplit('#', 1)[1])
        except (KeyError, IndexError, ValueError, AttributeError):
            pass
        usage = usage_for(idx)
        if body.get('stream'):
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            for ev in (
                {'choices': [{'delta': {'role': 'assistant', 'content': '账'}}]},
                {'choices': [{'delta': {'content': '单'}}]},
            ):
                self.wfile.write(b'data: ' + json.dumps(ev).encode() + b'\n\n')
            tail = {'choices': [{'delta': {}, 'finish_reason': 'stop'}]}
            if usage:
                tail['usage'] = {'prompt_tokens': usage[0], 'completion_tokens': usage[1],
                                 'total_tokens': usage[0] + usage[1]}
            self.wfile.write(b'data: ' + json.dumps(tail).encode() + b'\n\n')
            self.wfile.write(b'data: [DONE]\n\n')
            return
        out = {'id': 'cmpl-bill', 'object': 'chat.completion', 'model': body.get('model', ''),
               'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'mock'},
                            'finish_reason': 'stop'}]}
        if usage:
            out['usage'] = {'prompt_tokens': usage[0], 'completion_tokens': usage[1],
                            'total_tokens': usage[0] + usage[1]}
        payload = json.dumps(out).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def start_mock():
    srv = ThreadingHTTPServer(('127.0.0.1', MOCK_PORT), MockHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


# ---------------- 价格计划（点/百万 token） ----------------
P1 = (1_111_111, 3_333_333)     # bill-a 初价（非整除，打 ceil 边界）
P2 = (2_000_000, 8_000_000)     # B 段调价后
O1 = (999_999, 2_999_997)       # C 段 org1 客户覆盖价
PX = (500_000, 1_500_000)       # bill-x 默认价（org2 用，验证覆盖价不串客户）
COST_P = (555_555, 1_777_777)   # 成本价（全程不变）


def chat(model, key, idx, stream=False):
    body = {'model': model, 'max_tokens': 64,
            'messages': [{'role': 'user', 'content': f'bill {TS} #{idx}'}]}
    if stream:
        body['stream'] = True
    st, h, r = call('POST', '/v1/chat/completions', key=key, body=body)
    return st, h, r


def main():
    start_mock()
    admin = login('admin', 'admin123456')

    ma, mb, mx = f'bill-a-{TS}', f'bill-b-{TS}', f'bill-x-{TS}'
    # 模型：售卖价 + 成本价（vendor 对账用）
    for name, ip, op in ((ma, *P1), (mb, *P2), (mx, *PX)):
        st, _, r = call('POST', '/api/platform/models', admin, {
            'name': name, 'vendor': 'bill', 'input_price': ip, 'output_price': op,
            'cost_input_price': COST_P[0], 'cost_output_price': COST_P[1]})
        assert st == 200, (name, st, r)

    # 客户：org1 零额度起步（全部授权发生在 10 月内，勾稽链才闭合）；org2 交叉验证
    st, _, r = call('POST', '/api/platform/orgs', admin, {
        'name': f'bill-org-{TS}', 'quota_amount': 0,
        'admin_username': f'billa{TS}', 'admin_password': 'BillPass123'})
    assert st == 200, r
    org1 = r['org']['id'] if 'org' in r else r['id']
    tok1 = login(f'billa{TS}', 'BillPass123')
    st, _, r = call('POST', '/api/platform/orgs', admin, {
        'name': f'bill-org2-{TS}', 'quota_amount': 10_000_000,
        'admin_username': f'billb{TS}', 'admin_password': 'BillPass123'})
    assert st == 200, r
    org2 = r['org']['id'] if 'org' in r else r['id']

    # 客户级模型开通（须在建渠道前——渠道事件会联动停用无上游模型）
    st, _, r = call('PUT', f'/api/platform/orgs/{org1}/models', admin, {'models': [ma, mb, mx]})
    assert st == 200, r
    st, _, r = call('PUT', f'/api/platform/orgs/{org2}/models', admin, {'models': [mx]})
    assert st == 200, r

    def mk_member(otok, username, quota, models):
        st, _, r = call('POST', '/api/org/members', otok,
                        {'username': username, 'password': 'BillPass123', 'quota_amount': quota})
        assert st == 200, (username, st, r)
        st, _, r = call('PUT', f"/api/org/members/{r['id']}/models", otok, {'models': models})
        assert st == 200, r
        st, _, r = call('POST', '/api/member/keys', login(username, 'BillPass123'), {'name': 'bill'})
        assert st == 200 and r.get('key'), r
        return r['key']

    k1 = mk_member(tok1, f'billm{TS}', 700_000_000, [ma])       # 主力
    k2 = mk_member(tok1, f'billalert{TS}', 600_000, [mb])       # 预警
    k3 = mk_member(tok1, f'billov{TS}', 700_000, [mb])          # 超扣
    k4 = mk_member(login(f'billb{TS}', 'BillPass123'), f'billx{TS}', 5_000_000, [mx])  # 跨客户

    # 渠道（双 Key）：三个模型全挂
    st, _, r = call('POST', '/api/platform/channels', admin, {
        'name': f'bill-ch-{TS}', 'vendor': 'bill', 'base_url': f'http://127.0.0.1:{MOCK_PORT}',
        'upstream_key': 'k-bill-1\nk-bill-2',
        'models': [{'model_name': m} for m in (ma, mb, mx)]})
    assert st == 200, r
    ch = r['id']

    month = time.strftime('%Y-%m')
    prev_month = (datetime.now().replace(day=1) - timedelta(days=1)).strftime('%Y-%m')

    # 期初快照（上月末）：org1 限额 0 / 已用 0 —— 期内全部授权都发生在本月，链才闭合
    st, _, r = call('POST', '/api/platform/billing/snapshots', admin, {'period': prev_month})
    assert st == 200, r

    # 期内授权①：期初注资
    st, _, r = call('POST', f'/api/platform/orgs/{org1}/quota', admin,
                    {'amount': 500_000_000, 'remark': '期初注资'})
    assert st == 200, r

    # ---- 账本（脚本侧独立期望）：(member, model, idx, 价, 流式) ----
    ledger = []

    def fire(key, model, idx_range, price, stream=False, member='m1'):
        for i in idx_range:
            st, h, r = chat(model, key, i, stream=stream)
            assert st == 200, (i, st, r)
            ledger.append((member, model, i, price))

    # A 段：初价 P1，40 非流式 + 8 流式
    fire(k1, ma, range(0, 40), P1)
    fire(k1, ma, range(40, 48), P1, stream=True)

    # 被拒尝试：404 不存在 / 403 未授权 / 400 非法 JSON（零成本行，不得进账单明细）
    st, _, _ = chat('no-such-model', k1, 999)
    check('P0a 不存在模型 404', st == 404, f'got {st}')
    st, _, _ = chat(mx, k1, 998)
    check('P0b 未授权模型 403', st == 403, f'got {st}')
    st, _, _ = call('POST', '/v1/chat/completions', key=k1, raw=b'{not json')
    check('P0c 非法 JSON 400', st == 400, f'got {st}')

    # B 段：调价 P2（只改两价，历史行快照不受影响）
    st, _, r = call('GET', '/api/platform/models', admin)
    mlst = r.get('list') if isinstance(r, dict) else r
    ma_id = next(x['id'] for x in mlst if x['name'] == ma)
    st, _, r = call('PUT', f'/api/platform/models/{ma_id}', admin,
                    {'input_price': P2[0], 'output_price': P2[1]})
    assert st == 200, r
    fire(k1, ma, range(48, 88), P2)

    # 期内授权②：追充
    st, _, r = call('POST', f'/api/platform/orgs/{org1}/quota', admin,
                    {'amount': 200_000_000, 'remark': '中途追充'})
    assert st == 200, r

    # C 段：org1 客户覆盖价 O1（org2 不受影响）
    st, _, r = call('PUT', f'/api/platform/model-prices/{ma}', admin, {
        'org_id': org1, 'input_price': O1[0], 'output_price': O1[1], 'remark': '大客户价'})
    assert st == 200, r
    fire(k1, ma, range(88, 118), O1)

    # 跨客户：org2 打 mx 走默认价（覆盖价不串客户）
    fire(k4, mx, range(118, 128), PX, member='m4')

    # 精确缓存：同 body 两次 → 第二次命中 cost=0
    st1, h1, _ = chat(ma, k1, 200)
    st2, h2, _ = chat(ma, k1, 200)
    hit = h2.get('X-Tg-Cache') == 'hit' or h1.get('X-Tg-Cache') == 'hit'
    check('R10a 精确缓存第二次命中', hit, f'{h1.get("X-Tg-Cache")}/{h2.get("X-Tg-Cache")}')
    ledger.append(('m1', ma, 200, O1))  # 命中行 cost=0，由 cache_hit 标记区分

    # no_usage 批次：上游不回 usage → 不计费但披露
    fire(k1, ma, range(210, 215), O1)

    # 预警：billalert 一笔 520,000 点 / 限额 600,000 = 86.7% → 越 80% 线
    st, _, _ = chat(mb, k2, 300)
    assert st == 200, st
    ledger.append(('m2', mb, 300, P2))
    alerted = None
    for _ in range(80):
        time.sleep(1)
        st, _, r = call('GET', '/api/me/notifications', tok1)
        items = r.get('list') if isinstance(r, dict) else r
        alerted = next((n for n in (items or [])
                        if n.get('type') == 'quota_alert' and f'billalert{TS}' in (n.get('title') or '')), None)
        if alerted:
            break
    check('R11 余额预警：越 80% 线客户管理员收站内信',
          alerted is not None and '80%' in (alerted.get('title') or ''), f'{alerted}')

    # 超扣：billov 两笔 520,000（限额 700,000）→ 第三笔 402；超扣封顶 ≤ 单笔
    ov_unit = ceil_m(SPECIAL[300][0] * P2[0] + SPECIAL[300][1] * P2[1])
    for i in (301, 302):
        st, _, _ = chat(mb, k3, i)
        assert st == 200, (i, st)
        ledger.append(('m3', mb, i, P2))
    st, _, r = chat(mb, k3, 303)
    ecode = (r.get('error') or {}).get('code') if isinstance(r, dict) else None
    check('R12a 额度耗尽第三笔 402', st == 402 and ecode == 'insufficient_balance', f'{st} {ecode}')

    # ---- 期末快照（本月） ----
    st, _, r = call('POST', '/api/platform/billing/snapshots', admin, {'period': month})
    assert st == 200, r

    # ================= 对账 =================
    uid = lambda name: db_rows('SELECT id FROM users WHERE username=?', (name,))[0][0]
    m1, m2, m3, m4 = uid(f'billm{TS}'), uid(f'billalert{TS}'), uid(f'billov{TS}'), uid(f'billx{TS}')

    rows = db_rows('''SELECT id, user_id, model_name, prompt_tokens, completion_tokens, is_stream,
                             input_price, output_price, cost_input_price, cost_output_price,
                             vendor_cost, cost, no_usage, cache_hit, status
                      FROM usage_logs WHERE model_name IN (?,?,?) AND created_at >= ?
                      ORDER BY id''', (ma, mb, mx, int(time.time()) - 3600))
    check('R5a 账单行数非空且有流量', len(rows) >= 120, f'{len(rows)} 行')

    # 成功行与账本逐行对齐（id 升序 == 打点顺序）
    events = iter(ledger)
    exp_by_member = {'m1': 0, 'm2': 0, 'm3': 0, 'm4': 0}
    bad_formula, bad_snapshot, bad_vendor, unmatched = [], [], [], []
    n_cache, n_no_usage, n_failed = 0, 0, 0
    failed_detail = []
    for row in rows:
        (rid, uid_, model, pt, ct, is_stream, ip, op, cip, cop, vc, cost, nou, chit, status) = row
        if status != 200:
            n_failed += 1
            failed_detail.append((model, status, cost))
            if cost != 0:
                bad_formula.append((rid, '失败行有成本', cost))
            continue
        if chit == 1:      # 精确缓存命中：cost=0 不耗上游，不在账本（不消耗事件）
            n_cache += 1
            if cost != 0:
                bad_formula.append((rid, '缓存命中应 cost=0', cost))
            continue
        ev = next(events, None)
        if ev is None:
            unmatched.append((rid, '多余成功行'))
            continue
        member, ev_model, idx, price = ev
        if nou == 1:       # 上游漏 usage：不计费（tokens 快照无意义，只查零成本）
            n_no_usage += 1
            if (pt, ct) != (0, 0) or cost != 0:
                bad_formula.append((rid, 'no_usage 行应零 token 零成本', (pt, ct, cost)))
        else:
            tin, tout = usage_for(idx)
            exp_cost = ceil_m(pt * ip + ct * op)
            if (model, pt, ct) != (ev_model, tin, tout) or cost != exp_cost:
                bad_formula.append((rid, f'tokens/cost 期望 {(ev_model, tin, tout, exp_cost)}', (model, pt, ct, cost)))
            if (ip, op) != price:
                bad_snapshot.append((rid, f'价格快照期望 {price}', (ip, op)))
            exp_vc = ceil_m(pt * cip + ct * cop)
            if vc != exp_vc:
                bad_vendor.append((rid, f'vendor 期望 {exp_vc}', vc))
            if (cip, cop) != COST_P:  # 价格快照只随 applyUsage 落列；no_usage 行无快照
                bad_vendor.append((rid, '成本价快照漂移', (cip, cop)))
        exp_by_member[member] += cost
    if next(events, None) is not None:
        unmatched.append(('EOF', '账本还有未落账的请求'))
    check('R1 每行 cost/tokens 精确（ceil 公式）', not bad_formula, f'{bad_formula[:3]}')
    check('R2 价格快照随时间线正确（P1→P2→O1/默认）', not bad_snapshot, f'{bad_snapshot[:3]}')
    check('R3 每行 vendor_cost 精确 + 成本价不漂移', not bad_vendor, f'{bad_vendor[:3]}')
    check('R5b 成功行与账本一一对应（无缺无重）', not unmatched, f'{unmatched[:3]}')
    check('R10b 缓存命中 1 行 cost=0', n_cache == 1, f'{n_cache}')
    check('R9a no_usage 5 行零成本', n_no_usage == 5, f'{n_no_usage}')
    check('R10c 被拒尝试零成本（403 未授权 / 402 超扣）',
          n_failed == 2 and all(c == 0 for _, _, c in failed_detail), f'{failed_detail}')
    rej = db_rows('''SELECT model_name, status, cost FROM usage_logs
                     WHERE status != 200 AND cost >= 0 AND model_name NOT IN (?,?,?)
                       AND created_at >= ?''', (ma, mb, mx, int(time.time()) - 3600))
    check('R10d 预检拒绝行零成本（404 不存在 / 400 非法体）',
          len(rej) == 2 and all(r[2] == 0 for r in rej), f'{rej}')

    # 双记账：users / orgs.quota_used == Σ行 cost（逐 member 精确）
    for name, mid, exp in ((f'billm{TS}', m1, exp_by_member['m1']), (f'billx{TS}', m4, exp_by_member['m4'])):
        used = db_rows('SELECT quota_used FROM users WHERE id=?', (mid,))[0][0]
        check(f'R4a 子账号 {name} quota_used 精确', used == exp, f'{used} vs {exp}')
    billalert_used = db_rows('SELECT quota_used FROM users WHERE id=?', (m2,))[0][0]
    check('R4b 预警子账号已用 == 520,000', billalert_used == 520_000, f'{billalert_used}')
    ov_used = db_rows('SELECT quota_used FROM users WHERE id=?', (m3,))[0][0]
    check('R12b 超扣已用 == 2×520,000 精确', ov_used == 2 * ov_unit, f'{ov_used} vs {2 * ov_unit}')
    check('R12c 超扣封顶 ≤ 单笔成本', 0 < ov_used - 700_000 <= ov_unit, f'{ov_used - 700_000} vs {ov_unit}')
    for oid, exp in ((org1, exp_by_member['m1'] + exp_by_member['m2'] + exp_by_member['m3']),
                     (org2, exp_by_member['m4'])):
        used = db_rows('SELECT quota_used FROM orgs WHERE id=?', (oid,))[0][0]
        check(f'R4c org{oid} quota_used == Σ成员(双记账)', used == exp, f'{used} vs {exp}')

    # Σgrants == quota_limit（org1 含期初注资+追充；org2 含初始额度）
    for stype, sid, tbl in (('org', org1, 'orgs'), ('org', org2, 'orgs'),
                            ('user', m1, 'users'), ('user', m3, 'users')):
        limit, grants = db_rows(
            f'SELECT quota_limit, (SELECT COALESCE(SUM(amount),0) FROM quota_grants g WHERE g.subject_type=? AND g.subject_id={tbl}.id) FROM {tbl} WHERE id=?',
            (stype, sid))[0]
        check(f'R6 Σgrants==limit（{stype}#{sid}）', limit == grants, f'{limit} vs {grants}')

    # 账单勾稽（服务端聚合 + 快照链）
    st, _, r = call('GET', f'/api/org/billing/statement?month={month}', tok1)
    assert st == 200, r
    exp_c1 = exp_by_member['m1'] + exp_by_member['m2'] + exp_by_member['m3']
    checks = [
        ('R7a 期初限额 0（零额度起步）', r.get('opening_limit') == 0, r.get('opening_limit')),
        ('R7b 期初已用 0', r.get('opening_used') == 0, r.get('opening_used')),
        ('R7c 期内授权 Σ == 700,000,000', r.get('total_granted') == 700_000_000, r.get('total_granted')),
        ('R7d 消耗 == 脚本独立期望 Σ', r.get('consumption') == exp_c1, f"{r.get('consumption')} vs {exp_c1}"),
        ('R7e 期末限额 700,000,000', r.get('closing_limit') == 700_000_000, r.get('closing_limit')),
        ('R7f 期末已用 == 消耗（零起步）', r.get('closing_used') == exp_c1, r.get('closing_used')),
        ('R7g 服务端勾稽 chain_ok', r.get('chain_ok') is True, r.get('chain_ok')),
        ('R7h no_usage 披露 5 笔', r.get('no_usage_count') == 5, r.get('no_usage_count')),
        ('R7i 折元汇率快照 1,000,000', r.get('points_per_yuan') == 1_000_000, r.get('points_per_yuan')),
    ]
    for name, cond, detail in checks:
        check(name, cond, detail)
    # 月初余额 + 期内授权 − 消耗 == 月末余额
    open_bal = r.get('opening_limit', 0) - r.get('opening_used', 0)
    close_bal = r.get('closing_limit', 0) - r.get('closing_used', 0)
    check('R7j 期初余额+授权−消耗==期末余额',
          open_bal + r.get('total_granted', 0) + r.get('total_revoked', 0) - r.get('consumption', 0) == close_bal,
          f'{open_bal}+{r.get("total_granted", 0)}{r.get("total_revoked", 0)}-{r.get("consumption", 0)} vs {close_bal}')

    # 平台侧客户账单一致（含厂商视角）
    st, _, r2 = call('GET', f'/api/platform/orgs/{org1}/statement?month={month}', admin)
    check('R7k 平台侧账单一致（消耗/勾稽）',
          st == 200 and r2.get('consumption') == exp_c1 and r2.get('chain_ok') is True,
          f"{st} {r2.get('consumption')} vs {exp_c1} {r2.get('chain_ok')}")

    # 管理面月度总览
    st, _, r3 = call('GET', f'/api/org/billing?month={month}', tok1)
    sm = (r3 or {}).get('summary') or {}
    check('R7l 月度汇总 cost 一致', st == 200 and sm.get('cost') == exp_c1, f'{sm}')

    # 厂商账单对账：先按精确 Σvendor_cost 录入 → diff 0；再篡改 → diff 精确
    vc_sum = db_rows('SELECT COALESCE(SUM(vendor_cost),0) FROM usage_logs WHERE channel_id=?', (ch,))[0][0]
    st, _, r = call('PUT', '/api/platform/vendor-bills', admin,
                    {'period': month, 'channel_id': ch, 'billed_points': vc_sum, 'note': '精确对账'})
    assert st == 200, r
    st, _, r = call('GET', f'/api/platform/vendor-bills?period={month}', admin)
    items = r.get('list') if isinstance(r, dict) else r
    row = next((x for x in (items or []) if x.get('channel_id') == ch), None)
    check('R8a 厂商账单精确对账 diff==0', row is not None and row.get('diff') == 0 and not row.get('over_pct'),
          f'{row}')
    tampered = vc_sum + 12_345
    st, _, r = call('PUT', '/api/platform/vendor-bills', admin,
                    {'period': month, 'channel_id': ch, 'billed_points': tampered, 'note': '篡改'})
    st, _, r = call('GET', f'/api/platform/vendor-bills?period={month}', admin)
    items = r.get('list') if isinstance(r, dict) else r
    row = next((x for x in (items or []) if x.get('channel_id') == ch), None)
    check('R8b 篡改后 diff 精确 == -12,345', row is not None and row.get('diff') == -12_345,
          f"diff={row and row.get('diff')}")

    # CSV 对账单：行存在 + 总额（元）与点数一致
    st, _, csv = call('GET', f'/api/org/billing/statement/csv?month={month}', tok1)
    if isinstance(csv, str) and st == 200:
        total_line = [ln for ln in csv.splitlines() if '合计' in ln]
        exp_yuan = f'{exp_c1 / 1_000_000:.2f}'
        check('R12d CSV 对账单总额（元）一致', any(exp_yuan in ln for ln in total_line),
              f'{total_line[:2]} 期望含 {exp_yuan}')
    else:
        check('R12d CSV 对账单总额（元）一致', False, f'CSV 拉取失败 {st}')

    # ---- 清理 ----
    def db_exec(sql, args=()):
        conn = sqlite3.connect(DB_PATH, timeout=10)
        conn.execute(sql, args)
        conn.commit()
        conn.close()

    for oid in (org1, org2):
        db_exec('DELETE FROM quota_grants WHERE subject_type=? AND subject_id=?', ('org', oid))
        st, _, _ = call('DELETE', f'/api/platform/orgs/{oid}', admin)
    db_exec('DELETE FROM vendor_bills WHERE channel_id=?', (ch,))
    call('DELETE', f'/api/platform/channels/{ch}', admin)
    for m in (ma, mb, mx):
        row = db_rows('SELECT id FROM models WHERE name=?', (m,))
        if row:
            call('DELETE', f"/api/platform/models/{row[0][0]}", admin)
    db_exec('DELETE FROM usage_logs WHERE model_name LIKE ?', (f'bill-%{TS}',))
    left = db_rows('SELECT COUNT(*) FROM usage_logs WHERE model_name LIKE ?', (f'bill-%{TS}',))[0][0]
    check('Z 清理残留清零', left == 0, f'left={left}')

    fails = [r for r in results if r[0] == 'FAIL']
    print(f'\n==== {len(results) - len(fails)}/{len(results)} 通过，{len(fails)} 失败 ====')
    for _, name, detail in fails:
        print(f'  FAIL {name} — {detail}')
    sys.exit(1 if fails else 0)


if __name__ == '__main__':
    main()
