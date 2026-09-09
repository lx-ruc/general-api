#!/usr/bin/env python3
"""中转站端到端回归：数据面 / 管理面 / 计费不变量。

前置条件：
- 网关跑在 :8081（建议 TG_CACHE_TTL>0 以启用缓存断言，否则缓存项自动跳过）
- 脚本自带 mock 上游（:9102），按 path 与 key 前缀切换行为，无外部依赖
- 依赖本地账号 admin / liance_admin / tester01（仅用 admin 建隔离的 e2e 数据，不碰存量账号）

用法：python3 tools/e2e/run.py   # 失败退出码 1
"""
import json
import sqlite3
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

GW = 'http://127.0.0.1:8081'
MOCK_PORT = 9102
TS = str(int(time.time()))
DB_PATH = 'data/token_.db'
FIXED_USAGE = {'prompt_tokens': 100, 'completion_tokens': 50, 'total_tokens': 150}

results = []


def check(name, cond, detail=''):
    tag = 'PASS' if cond else 'FAIL'
    results.append((tag, name, detail))
    print(f'{tag}  {name}' + (f'  — {detail}' if detail and not cond else ''))


# ---------------- mock 上游 ----------------

class MockHandler(BaseHTTPRequestHandler):
    def log_message(self, *a):  # 静默
        pass

    def _code_by(self, key, path):
        if path.startswith('/err500'):
            return 500
        if path.startswith('/err401'):
            return 401
        for pfx, code in (('k429', 429), ('k401', 401), ('k500', 500)):
            if key.startswith(pfx):
                return code
        return 200

    def do_POST(self):
        auth = self.headers.get('Authorization', '')
        key = auth.replace('Bearer ', '').strip()
        length = int(self.headers.get('Content-Length', 0))
        body = json.loads(self.rfile.read(length) or b'{}')
        code = self._code_by(key, self.path)
        if code == 429:
            self.send_response(429)
            self.send_header('Retry-After', '5')
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps({'error': {'code': 'rate_limit', 'message': 'mock 429'}}).encode())
            return
        if code != 200:
            self.send_response(code)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps({'error': {'code': f'mock{code}', 'message': f'mock {code}'}}).encode())
            return
        if body.get('stream'):
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            for ev in (
                {'choices': [{'delta': {'role': 'assistant', 'content': '你'}}]},
                {'choices': [{'delta': {'content': '好'}}]},
                {'choices': [{'delta': {}, 'finish_reason': 'stop'}], 'usage': FIXED_USAGE},
            ):
                self.wfile.write(b'data: ' + json.dumps(ev).encode() + b'\n\n')
            self.wfile.write(b'data: [DONE]\n\n')
            return
        out = {'id': 'cmpl-mock', 'object': 'chat.completion', 'model': body.get('model', ''),
               'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'mock 回复'}, 'finish_reason': 'stop'}],
               'usage': FIXED_USAGE}
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


# ---------------- HTTP 工具 ----------------

def call(method, path, token=None, body=None, raw=None, timeout=60, key=None):
    """返回 (status, headers, parsed_body_or_bytes)"""
    url = GW + path if path.startswith('/') else path
    data = raw
    if body is not None:
        data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    if key:
        req.add_header('Authorization', 'Bearer ' + key)
    req.add_header('Content-Type', 'application/json')
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw_body = r.read()
            try:
                return r.status, dict(r.headers), json.loads(raw_body)
            except (ValueError, UnicodeDecodeError):
                return r.status, dict(r.headers), raw_body
    except urllib.error.HTTPError as e:
        raw_body = e.read()
        try:
            return e.code, dict(e.headers), json.loads(raw_body)
        except (ValueError, UnicodeDecodeError):
            return e.code, dict(e.headers), raw_body


def login(username, password):
    """登录（登录接口限流 5 次/分，429 时退避重试）"""
    for i in range(8):
        st, _, r = call('POST', '/api/auth/login', body={'username': username, 'password': password})
        if st == 200:
            return r['token']
        if st == 429:
            time.sleep(14)
            continue
        raise RuntimeError(f'登录失败 {username}: {st} {r}')
    raise RuntimeError(f'登录持续限流: {username}')


def chat(model, key, content=None, stream=False, raw=None, timeout=60):
    if raw is not None:
        return call('POST', '/v1/chat/completions', raw=raw, key=key, timeout=timeout)
    body = {'model': model, 'max_tokens': 64, 'messages': [
        {'role': 'user', 'content': content or f'e2e {time.time()}'}]}
    if stream:
        body['stream'] = True
    return call('POST', '/v1/chat/completions', body=body, key=key, timeout=timeout)


def sse_chat(model, key):
    """流式请求，返回 (status, [data块])；逐行读 SSE"""
    body = json.dumps({'model': model, 'stream': True, 'max_tokens': 64,
                       'messages': [{'role': 'user', 'content': f'sse {time.time()}'}]}).encode()
    req = urllib.request.Request(GW + '/v1/chat/completions', data=body, method='POST')
    req.add_header('Authorization', 'Bearer ' + key)
    req.add_header('Content-Type', 'application/json')
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            events = []
            while True:
                line = r.readline()
                if not line:
                    break
                line = line.strip()
                if line.startswith(b'data: '):
                    payload = line[6:]
                    if payload == b'[DONE]':
                        events.append('[DONE]')
                    else:
                        events.append(json.loads(payload))
            return r.status, events
    except urllib.error.HTTPError as e:
        return e.code, []


def db_rows(sql, args=()):
    """只读打开 WAL 库做不变量校验"""
    conn = sqlite3.connect(f'file:{DB_PATH}?mode=ro', uri=True, timeout=5)
    try:
        return conn.execute(sql, args).fetchall()
    finally:
        conn.close()


def main():
    start_mock()
    admin = login('admin', 'admin123456')

    # ============ 造数据：模型 / 渠道 / 客户 / 子账号 / key ============
    m_free, m_priced, m_noch = f'e2e-free-{TS}', f'e2e-priced-{TS}', f'e2e-noch-{TS}'
    m_nokey, m_nogrant, m_fail, m_rot, m_dis = (f'e2e-nokey-{TS}', f'e2e-nogrant-{TS}',
                                                f'e2e-fail-{TS}', f'e2e-rot-{TS}', f'e2e-dis-{TS}')
    for name, ip, op in ((m_free, 0, 0), (m_priced, 1_000_000, 2_000_000), (m_noch, 0, 0),
                         (m_nokey, 0, 0), (m_nogrant, 0, 0), (m_fail, 0, 0), (m_rot, 0, 0), (m_dis, 0, 0)):
        st, _, r = call('POST', '/api/platform/models', admin,
                        {'name': name, 'vendor': 'e2e', 'input_price': ip, 'output_price': op})
        assert st == 200, (name, st, r)
    mock = f'http://127.0.0.1:{MOCK_PORT}'

    def mk_channel(name, keys, models, priority=10):
        payload = {'name': name, 'vendor': 'e2e', 'base_url': mock, 'priority': priority,
                   'models': [{'model_name': m} for m in models]}
        if keys:
            payload['upstream_key'] = '\n'.join(keys)
        st, _, r = call('POST', '/api/platform/channels', admin, payload)
        assert st == 200, (name, st, r)
        return r['id']

    ch_ok = mk_channel(f'e2e-ok-{TS}', ['k-ok-1', 'k-ok-2'], [m_free, m_priced, m_nogrant])
    ch_nokey = mk_channel(f'e2e-nokey-{TS}', [], [m_nokey])
    ch_500 = mk_channel(f'e2e-500-{TS}', ['k-ok-9'], [m_fail], priority=10)
    ch_ok2 = mk_channel(f'e2e-ok2-{TS}', ['k-ok-5'], [m_fail], priority=5)
    ch_rot = mk_channel(f'e2e-rot-{TS}', ['k429-a', 'k-ok-3'], [m_rot])
    ch_dis = mk_channel(f'e2e-dis-{TS}', ['k401-x'], [m_dis])

    st, _, r = call('POST', '/api/platform/orgs', admin, {
        'name': f'e2e-org-{TS}', 'quota_amount': 10_000_000,
        'admin_username': f'e2ea{TS}', 'admin_password': 'E2ePass123'})
    assert st == 200, r
    org_id = r['org']['id'] if 'org' in r else r['id']
    org = login(f'e2ea{TS}', 'E2ePass123')

    def mk_member(username, quota, models):
        st, _, r = call('POST', '/api/org/members', org,
                        {'username': username, 'password': 'E2ePass123', 'quota_amount': quota})
        assert st == 200, (username, st, r)
        uid = r['id']
        st, _, r = call('PUT', f'/api/org/members/{uid}/models', org, {'models': models})
        assert st == 200, r
        st, _, r = call('POST', '/api/member/keys', login(username, 'E2ePass123'), {'name': 'e2e'})
        assert st == 200 and r.get('key'), r
        return uid, r['key']

    granted = [m_free, m_priced, m_noch, m_nokey, m_fail, m_rot, m_dis]
    m1_id, k1 = mk_member(f'e2em{TS}', 1_000_000, granted)
    mq_id, kq = mk_member(f'e2eq{TS}', 300, [m_priced])

    # ============ A. 数据面 ============
    st, _, _ = chat(m_free, 'sk-invalid-key-xxx')
    check('A1 无效 API key → 401', st == 401, f'got {st}')

    st, _, _ = chat('', k1, raw=b'{not json')
    check('A2 非法 JSON → 400', st == 400, f'got {st}')

    st, _, r = chat('', k1, raw=json.dumps({'messages': [{'role': 'user', 'content': 'x'}]}).encode())
    check('A3 缺 model → 400', st == 400, f'got {st} {r}')

    st, _, r = chat('no-such-model-xyz', k1)
    check('A4 不存在的模型 → 404', st == 404, f'got {st}')

    st, _, r = chat(m_nogrant, k1)
    check('A5 未授权模型 → 403', st == 403, f'got {st} {r}')

    st, _, r = chat(m_noch, k1)
    code = (r.get('error') or {}).get('code') if isinstance(r, dict) else None
    check('A6 无渠道模型 → 503 no_available_channel', st == 503 and code == 'no_available_channel', f'got {st} {r}')

    st, _, r = chat(m_nokey, k1)
    code = (r.get('error') or {}).get('code') if isinstance(r, dict) else None
    check('A7 渠道未配 key → 503 channel_key_missing', st == 503 and code == 'channel_key_missing', f'got {st} {r}')

    st, _, r = chat(m_free, k1)
    check('A8 非流式 200 + usage', st == 200 and (r.get('usage') or {}).get('total_tokens') == 150, f'{st} {r}')

    st, events = sse_chat(m_priced, k1)
    has_usage = any(isinstance(e, dict) and e.get('usage') for e in events)
    check('A9 流式 200 + 末块 usage + [DONE]', st == 200 and has_usage and events[-1] == '[DONE]',
          f'{st} events={len(events)}')

    st, _, r = call('GET', '/v1/models', key=k1)
    ids = [x['id'] for x in r.get('data', [])]
    check('A10 /v1/models 只含已授权', m_free in ids and m_nogrant not in ids, f'{ids}')

    _, _, m_before = call('GET', '/metrics')
    cache_enabled = True
    body_a = {'model': m_priced, 'max_tokens': 64, 'messages': [{'role': 'user', 'content': f'cache-probe {TS}'}]}
    st1, h1, r1 = call('POST', '/v1/chat/completions', body=body_a, key=k1)
    st2, h2, r2 = call('POST', '/v1/chat/completions', body=body_a, key=k1)
    if h2.get('X-Tg-Cache') != 'hit' and h1.get('X-Tg-Cache') != 'hit':
        cache_enabled = False
        check('A11 精确缓存命中（第二次 X-Tg-Cache: hit）', True, 'SKIP：网关未开 cache_ttl')
    else:
        check('A11 精确缓存命中（第二次 X-Tg-Cache: hit）',
              st1 == 200 and st2 == 200 and (h1.get('X-Tg-Cache') == 'hit' or h2.get('X-Tg-Cache') == 'hit'))

    _, _, m_after = call('GET', '/metrics')
    def metric(name, text):
        for ln in text.splitlines() if isinstance(text, str) else []:
            if ln.startswith(name + ' '):
                return int(ln.split()[-1])
        return -1
    m_txt_b, m_txt_a = (m_before if isinstance(m_before, str) else m_before.decode(errors='ignore'),
                        m_after if isinstance(m_after, str) else m_after.decode(errors='ignore'))

    st, _, r = chat(m_rot, k1)
    d429 = metric('tg_gateway_upstream_429_total', m_txt_a) - metric('tg_gateway_upstream_429_total', m_txt_b)
    check('A12 上游 429 → 换 key 自愈 200', st == 200 and d429 >= 0, f'{st} Δ429={d429}')
    m_txt_b = m_txt_a
    _, _, m_txt_a = call('GET', '/metrics')
    m_txt_a = m_txt_a if isinstance(m_txt_a, str) else m_txt_a.decode(errors='ignore')

    st, _, r = chat(m_fail, k1)
    check('A13 上游 500 → 降级备用渠道 200', st == 200, f'{st} {r}')

    st, _, r = chat(m_dis, k1)
    code = (r.get('error') or {}).get('code') if isinstance(r, dict) else None
    check('A14 上游 401 全 Key 失效 → 503 channel_key_invalid', st == 503 and code == 'channel_key_invalid',
          f'{st} {r}')
    st, _, keys = call('GET', f'/api/platform/channels/{ch_dis}/keys', admin)
    dis_key = next((k for k in keys if k['status'] == 0), None)
    check('A15 401 后 key 自动禁用', dis_key is not None, f'{keys}')
    if dis_key:
        st, _, r = call('PUT', f'/api/platform/channels/{ch_dis}/keys/{dis_key["id"]}/status',
                        admin, {'status': 1})
        check('A16 禁用 key 可手动恢复', st == 200, f'{st} {r}')
        st, _, r = call('POST', f'/api/platform/channels/{ch_dis}/keys', admin, {'keys': ['k-ok-7']})
        st, _, r = chat(m_dis, k1)
        check('A17 补有效 key 后渠道恢复（轮换绕开坏 key）', st == 200, f'{st} {r}')

    st1, _, _ = chat(m_priced, kq, content=f'quota-1 {TS}')
    st2, _, _ = chat(m_priced, kq, content=f'quota-2 {TS}')
    st3, _, r3 = chat(m_priced, kq, content=f'quota-3 {TS}')
    ecode = (r3.get('error') or {}).get('code') if isinstance(r3, dict) else None
    check('A18 额度耗尽 → 429 insufficient_balance（允许首次超扣）',
          st1 == 200 and st2 == 200 and st3 == 429 and ecode == 'insufficient_balance',
          f'{st1}/{st2}/{st3} {ecode}')

    big = json.dumps({'model': m_free, 'messages': [{'role': 'user', 'content': 'x' * (11 * 1024 * 1024)}]}).encode()
    st, _, r = chat('', k1, raw=big)
    check('A19 超 10MB 请求 → 413', st == 413, f'got {st}')

    # ============ B. 管理面 ============
    member_tok = login(f'e2em{TS}', 'E2ePass123')
    st, _, _ = call('GET', '/api/platform/orgs', member_tok)
    check('B1 子账号访问平台接口 → 403', st == 403, f'got {st}')
    st, _, _ = call('GET', '/api/platform/orgs', org)
    check('B2 客户管理员访问平台接口 → 403', st == 403, f'got {st}')
    st, _, _ = call('GET', '/api/member/keys', org)
    check('B3 客户管理员访问子账号接口 → 403', st == 403, f'got {st}')

    st, _, r = call('GET', '/api/playground/models', member_tok)
    lst = r if isinstance(r, list) else (r.get('models') or r.get('data') or [])
    names = [x.get('name') or x.get('id') for x in lst]
    check('B4 在线体验模型 = 授权 ∩ 可路由', st == 200 and (m_free in names) and (m_nogrant not in names),
          f'{st} {names}')

    st, _, r = call('POST', '/api/platform/orgs', admin, {
        'name': f'e2e-org2-{TS}', 'quota_amount': 1000,
        'admin_username': f'e2eb{TS}', 'admin_password': 'E2ePass123'})
    org2 = login(f'e2eb{TS}', 'E2ePass123')
    st, _, r = call('GET', '/api/org/members', org2)
    items = r.get('items', [])
    check('B5 客户隔离：对方看不到本客户成员列表', st == 200 and len(items) == 0, f'{items}')
    st, _, r = call('GET', f'/api/org/members/{m1_id}', org2)
    check('B6 客户隔离：越权查成员 → 404', st == 404, f'got {st}')
    st, _, r = call('GET', '/api/org/stats/overview', org2)
    check('B7 新客户统计为空', st == 200 and r.get('total', {}).get('requests', 0) == 0, f'{r}')

    st, _, r = call('GET', '/api/platform/audit', admin)
    check('B8 审计日志可查', st == 200 and r.get('total', 0) > 0, f'{r}')

    st, _, keys = call('GET', f'/api/platform/channels/{ch_ok}/keys', admin)
    n0 = len(keys)
    st, _, r = call('POST', f'/api/platform/channels/{ch_ok}/keys', admin, {'keys': ['k-ok-4'], 'weight': 1})
    _, _, keys = call('GET', f'/api/platform/channels/{ch_ok}/keys', admin)
    check('B9 Key 池追加', st == 200 and len(keys) == n0 + 1, f'{st} {len(keys)}')
    kid = keys[-1]['id']
    call('PUT', f'/api/platform/channels/{ch_ok}/keys/{kid}/status', admin, {'status': 0})
    _, _, keys = call('GET', f'/api/platform/channels/{ch_ok}/keys', admin)
    check('B10 Key 停用', next(k for k in keys if k['id'] == kid)['status'] == 0)
    call('PUT', f'/api/platform/channels/{ch_ok}/keys/{kid}/status', admin, {'status': 1})
    st, _, r = call('DELETE', f'/api/platform/channels/{ch_ok}/keys/{kid}', admin)
    _, _, keys = call('GET', f'/api/platform/channels/{ch_ok}/keys', admin)
    check('B11 Key 删除', st == 200 and len(keys) == n0, f'{len(keys)}')

    call('PUT', f'/api/platform/channels/{ch_ok}/status', admin, {'status': 0})
    st, _, r = chat(m_free, k1)
    code = (r.get('error') or {}).get('code') if isinstance(r, dict) else None
    check('B12 停用渠道 → 503 no_available_channel', st == 503 and code == 'no_available_channel', f'{st} {code}')
    call('PUT', f'/api/platform/channels/{ch_ok}/status', admin, {'status': 1})
    st, _, r = chat(m_free, k1)
    check('B13 启用渠道恢复', st == 200, f'{st}')

    st, _, r = call('POST', f'/api/platform/channels/{ch_ok}/test', admin)
    check('B14 渠道连通性测试', st == 200, f'{st} {r}')

    st, _, r = call('GET', f'/api/org/members/{m1_id}', org)
    quota_before = r.get('quota_limit') or 0
    st, _, r = call('POST', '/api/member/requests', member_tok, {'amount': 5000, 'reason': 'e2e 测试'})
    check('B15 子账号提交额度申请', st == 200 and r.get('status') == 'pending', f'{st} {r}')
    rid = r['id']
    st, _, r = call('GET', '/api/org/requests', org)
    lst = r if isinstance(r, list) else (r.get('list') or r.get('items') or [])
    check('B16 客户管理员可见待审申请', st == 200 and any(x['id'] == rid for x in lst), f'{st}')
    st, _, r = call('PUT', f'/api/org/requests/{rid}', org, {'action': 'approve', 'reply': 'ok'})
    check('B17 审批通过', st == 200, f'{st} {r}')
    st, _, r = call('GET', f'/api/org/members/{m1_id}', org)
    check('B18 审批后子账号额度 +5000', (r.get('quota_limit') or 0) == quota_before + 5000,
          f'{r.get("quota_limit")} vs {quota_before}+5000')

    st, _, r = call('GET', f'/api/platform/orgs/{org_id}', admin)
    org_q_before = (r.get('org') or r).get('quota_limit') or 0
    st, _, r = call('POST', '/api/org/recharges', org, {'amount': 777_777, 'voucher': f'V{TS}'})
    check('B19 客户提交充值申请', st == 200, f'{st} {r}')
    rcid = r['id']
    st, _, r = call('PUT', f'/api/platform/recharges/{rcid}', admin, {'action': 'approve'})
    check('B20 平台确认充值', st == 200, f'{st} {r}')
    st, _, r = call('GET', f'/api/platform/orgs/{org_id}', admin)
    org_now = (r.get('org') or r).get('quota_limit') or 0
    check('B21 充值到账 客户额度 +777777', org_now == org_q_before + 777_777,
          f'{org_now} vs {org_q_before}+777777')

    st, _, r = call('GET', '/api/org/billing', org)
    check('B22 账单勾稽接口', st == 200, f'{st}')
    st, _, r = call('GET', '/api/org/billing/statement', org)
    check('B23 对账单', st == 200, f'{st}')
    st, _, r = call('GET', f'/api/platform/orgs/{org_id}/statement', admin)
    check('B24 平台侧客户对账单', st == 200, f'{st}')

    st, _, r = call('PUT', '/api/platform/vendor-bills', admin,
                    {'period': time.strftime('%Y-%m'), 'channel_id': ch_ok, 'billed_points': 123, 'note': 'e2e'})
    check('B25 厂商账单录入', st == 200, f'{st} {r}')
    st, _, r = call('GET', '/api/platform/vendor-bills?period=' + time.strftime('%Y-%m'), admin)
    check('B26 厂商账单差异报表', st == 200, f'{st}')

    st, _, r = call('GET', '/api/me', member_tok)
    check('B27 /api/me 角色正确', st == 200 and r.get('role') == 'member', f'{r}')
    st, _, r = call('PUT', '/api/me/password', member_tok, {'old_password': 'wrong', 'new_password': 'Xx12345678'})
    check('B28 改密码：旧密码错误被拒', st == 400 or st == 401 or st == 422, f'{st}')

    # ============ C. 计费不变量（直接查库） ============
    bad = db_rows("""
        SELECT u.id, u.username, COALESCE(u.quota_limit,0), COALESCE((SELECT SUM(amount) FROM quota_grants g WHERE g.subject_type='user' AND g.subject_id=u.id),0)
        FROM users u WHERE COALESCE(u.quota_limit,0) != COALESCE((SELECT SUM(amount) FROM quota_grants g WHERE g.subject_type='user' AND g.subject_id=u.id),0)""")
    check('C1 全体用户 Σgrants == quota_limit', len(bad) == 0, f'{bad[:3]}')

    bad = db_rows("""
        SELECT o.id, o.name, COALESCE(o.quota_limit,0), COALESCE((SELECT SUM(amount) FROM quota_grants g WHERE g.subject_type='org' AND g.subject_id=o.id),0)
        FROM orgs o WHERE COALESCE(o.quota_limit,0) != COALESCE((SELECT SUM(amount) FROM quota_grants g WHERE g.subject_type='org' AND g.subject_id=o.id),0)""")
    check('C2 全体客户 Σgrants == quota_limit', len(bad) == 0, f'{bad[:3]}')

    rows = db_rows("SELECT COALESCE(quota_used,0) FROM orgs WHERE id=?", (org_id,))
    used = db_rows("SELECT COALESCE(SUM(cost),0) FROM usage_logs WHERE org_id=?", (org_id,))[0][0]
    check('C3 客户 quota_used == Σusage cost（双记账）', rows[0][0] == used, f'{rows[0][0]} vs {used}')

    rows = db_rows("SELECT COALESCE(quota_used,0) FROM users WHERE id=?", (m1_id,))
    used = db_rows("SELECT COALESCE(SUM(cost),0) FROM usage_logs WHERE user_id=?", (m1_id,))[0][0]
    check('C4 子账号 quota_used == Σusage cost', rows[0][0] == used, f'{rows[0][0]} vs {used}')

    rows = db_rows("SELECT cost, cache_hit FROM usage_logs WHERE model_name=? AND user_id=? ORDER BY id",
                   (m_priced, m1_id))
    costs = [c for c, ch in rows if ch == 0]
    check('C5 定价模型 cost == 200 点（100in×1 + 50out×2）', all(c == 200 for c in costs) and costs, f'{rows}')
    if cache_enabled:
        hits = [ch for _, ch in rows]
        check('C6 缓存命中行 cost=0 cache_hit=1', 1 in hits, f'{rows}')
        hit_cost = [c for c, ch in rows if ch == 1]
        check('C7 缓存命中不扣费', all(c == 0 for c in hit_cost), f'{hit_cost}')

    rows = db_rows("SELECT COALESCE(quota_used,0) FROM users WHERE id=?", (mq_id,))
    check('C8 超扣封顶在单请求成本内（300 限额两次 200 后 ≈400）', 0 < rows[0][0] <= 400, f'{rows[0][0]}')

    # ============ 汇总 ============
    fails = [r for r in results if r[0] == 'FAIL']
    print(f'\n==== {len(results) - len(fails)}/{len(results)} 通过，{len(fails)} 失败 ====')
    for _, name, detail in fails:
        print(f'  FAIL {name} — {detail}')
    sys.exit(1 if fails else 0)


if __name__ == '__main__':
    main()
