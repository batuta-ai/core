#!/usr/bin/env python3
"""batuta watch — mock generator, revision 2 (after the 2026-09-06 critique).

States: calm, question, limit, blocked, conflict. Widths: 120, 80, 60.
Labels: en (default) or pt (LANG=pt_*); values stay as the journal has them.
Glyphs: unicode set or ascii fallback (no UTF-8 locale)."""
import unicodedata, sys, os

def w(s): return sum(2 if unicodedata.east_asian_width(c) in 'WF' else 1 for c in s)
def fit(s, n, align='<'):
    if w(s) > n:
        out = ''
        for c in s:
            if w(out + c) > n - 1: break
            out += c
        s = out + G['ell']
    pad = ' ' * (n - w(s))
    return s + pad if align == '<' else pad + s

UNI = dict(h='─', v='│', tl='┌', tr='┐', bl='└', br='┘', ok='✓', fail='✗', run='>', pend='·', na='—', q='?', wait='~', conflict='<', esc='^', full='█', empty='░', ell='…', arrow='→', up='^', down='v')
ASC = dict(h='-', v='|', tl='+', tr='+', bl='+', br='+', ok='+', fail='x', run='>', pend='.', na='-', q='?', wait='~', conflict='<', esc='^', full='#', empty='.', ell='~', arrow='->', up='^', down='v')
G = UNI

L = {
 'en': dict(context='Context', progress='Progress', detail='Detail', table='Waves and tasks', logs='Recent log', waves='Waves', tasks='Tasks', sessions='sessions', retries='retries', escalations='escalations', keys='up/down PgUp/PgDn scroll · f follow active · r answer · o open log · ? legend · q quit watch', above='above', below='below', running='running', integrated='integrated', pending='pending', executor='executor', question='waiting answer', limit='usage limit', blocked='blocked', conflict='re-executing', escalated='escalated', attempt='Attempt', tr='Try', status='Status', gates='Gates', commit='Commit', id='ID', task='Wave / Task', criterion='Criterion', last='Last', reason='Reason', worktree='Worktree', log='Log', needs_none='needs you: no', tests='Tests', base='base', after='after', resumes='resumes', answer='r answers', openlog='o opens the log', phase='phase'),
 'pt': dict(context='Contexto', progress='Progresso', detail='Detalhe', table='Waves e tasks', logs='Log recente', waves='Waves', tasks='Tasks', sessions='sessões', retries='retries', escalations='escalações', keys='↑↓ PgUp/PgDn rolam · f segue a ativa · r responde · o abre o log · ? legenda · q sai do watch', above='acima', below='abaixo', running='em execução', integrated='integrada', pending='pendente', executor='executor', question='aguarda resposta', limit='limite de uso', blocked='bloqueada', conflict='reexecutando', escalated='escalada', attempt='Tentativa', tr='Tent.', status='Status', gates='Gates', commit='Commit', id='ID', task='Wave / Task', criterion='Critério', last='Último', reason='Motivo', worktree='Worktree', log='Log', needs_none='precisa de você: não', tests='Testes', base='base', after='após', resumes='retoma', answer='r responde', openlog='o abre o log', phase='fase'),
}

def box(title, rows, width):
    inner = width - 2
    top = G['tl'] + G['h'] + ' ' + title + ' ' + G['h'] * (inner - w(title) - 3) + G['tr']
    body = [G['v'] + ' ' + fit(r, inner - 2) + ' ' + G['v'] for r in rows]
    return [top] + body + [G['bl'] + G['h'] * inner + G['br']]
def bar(done, total, n):
    f = int(round(n * done / total)); return '[' + G['full'] * f + G['empty'] * (n - f) + ']'

def scenario(state, t):
    """Returns header, attention line, context rows, progress rows, detail rows, table rows, log lines."""
    ok, run, pend, na, fail = G['ok'], G['run'], G['pend'], G['na'], G['fail']
    gates_done = ok * 4; gates_none = pend * 4
    rows = [
        ('W1', f"{t['base']} 838d06a {G['arrow']} {t['integrated']} 4c1e9f2", f"{ok} 1/1", '', '', ''),
        ('task_1', 'Parse .batuta/roadmap.md into phases with an optional plan slug', f"{ok} {t['integrated']}", 'e1/4', gates_done, '4c1e9f2'),
        ('W2', f"{t['base']} 4c1e9f2", f"{run} 0/1", '', '', ''),
        ('task_2', 'Archiving a plan ticks its phase in the roadmap', f"{run} {t['executor']}", 'e1/4', gates_none, ''),
        ('W3', f"{t['after']} task_1", f"{pend} 0/1", '', '', ''),
        ('task_3', 'The opened record carries roadmap and phase', f"{pend} {t['pending']}", '', gates_none, ''),
        ('W4', f"{t['after']} task_2, task_3", f"{pend} 0/2", '', '', ''),
        ('task_4', 'batuta loop --roadmap runs the phases in order, one delivery per approved plan', f"{pend} {t['pending']}", '', gates_none, ''),
        ('task_5', 'capabilities, usage and docs describe the roadmap', f"{pend} {t['pending']}", '', gates_none, ''),
    ]
    attention = ''
    detail = [f"task_2 · e1/4 · Archiving a plan ticks its phase in the roadmap",
              f"{t['criterion']}  2/3 · TickPhase rewrites only the line",
              f"{t['last']}  task_progress 2 START · 12s",
              f"{t['worktree']}  .batuta/worktrees/roadmap-task-2-e1",
              f"{t['log']}  .batuta/runs/2026-09-06-roadmap-task-2-e1.out.log"]
    logs = ['BATUTA-PROGRESS 1 DONE', 'BATUTA-PROGRESS 2 START', 'exec: go test ./routing -run TestTickPhaseRewritesOnlyTheLine -count=1', '--- FAIL: TestTickPhaseRewritesOnlyTheLine (0.00s)', '    roadmap_test.go:88: TickPhase() rewrote 2 lines, want 1', 'codex: the rewrite must keep every other byte; switching to a line-indexed replace']
    status = f"{run} {t['running']} · 00:14:32"
    if state == 'question':
        rows[3] = ('task_2', rows[3][1], f"{G['q']} {t['question']}", 'e1/4', gates_none, '')
        attention = f"{G['q']} task_2 {t['question']} · \"Should TickPhase also archive the phase when every task is ticked?\" · {t['answer']}"
        detail[1] = f"{t['question']}  Should TickPhase also archive the phase when every task is ticked?"
        detail[2] = f"{t['last']}  question · 3m"
        status = f"{G['q']} {t['question']} · 00:14:32"
        logs = logs[:2] + ['BATUTA-QUESTION: Should TickPhase also archive the phase when every task is ticked?']
    elif state == 'limit':
        rows[3] = ('task_2', rows[3][1], f"{G['wait']} {t['limit']}", 'e1/4', gates_none, '')
        attention = f"{G['wait']} codex {t['limit']} · {t['resumes']} 23:40 (41 min) · wait 1/20 · same attempt, no retry spent"
        detail[2] = f"{t['last']}  limit_wait · 4m"
        status = f"{G['wait']} {t['limit']} · 00:14:32"
        logs = logs[:2] + ['Rate limit reached for gpt-5.6-sol, resets 11:40pm']
    elif state == 'blocked':
        rows[2] = ('W2', f"{t['base']} 4c1e9f2", f"{fail} 0/1", '', '', '')
        rows[3] = ('task_2', rows[3][1], f"{fail} {t['blocked']}", 'e3/4', ok + ok + fail + pend, '')
        rows[6] = ('W4', f"{t['after']} task_2, task_3", f"{fail} 0/2", '', '', '')
        attention = f"{fail} task_2 {t['blocked']} · G2 {t['tests']} failed 3/3 · {t['escalated']} gpt-5.6-sol {G['arrow']} gpt-6-astra · {t['openlog']}"
        detail[1] = f"{t['reason']}  G2 tests: TestTickPhaseRewritesOnlyTheLine fails after retry and escalation"
        detail[2] = f"{t['last']}  task_blocked · 1m"
        status = f"{fail} {t['blocked']} · 00:41:10"
        logs = ['--- FAIL: TestTickPhaseRewritesOnlyTheLine (0.00s)', '    roadmap_test.go:88: TickPhase() rewrote 2 lines, want 1', 'FAIL', 'FAIL\tgithub.com/batuta-ai/core/routing\t0.412s', 'gate 2 tests: fail (exit 1)', 'attempt e3/4 blocked: tests_failed']
    elif state == 'conflict':
        rows[3] = ('task_2', rows[3][1], f"{G['conflict']} {t['conflict']}", 'e2/4', gates_done, '')
        attention = f"{G['conflict']} task_2 conflict when integrating after task_3 · re-executing on 9b2c4e1 with the same executor · e2/4"
        detail[0] = 'task_2 · e2/4 · Archiving a plan ticks its phase in the roadmap'
        detail[1] = f"{t['reason']}  candidate 7fa1e02 conflicted with loop/report.go on 9b2c4e1"
        detail[2] = f"{t['last']}  settled reexecute_conflict · 20s"
        status = f"{G['conflict']} {t['conflict']} · 00:22:05"
    return status, attention, detail, rows, logs

def render(state, width, lang, glyphs):
    global G; G = UNI if glyphs == 'unicode' else ASC
    t = L[lang]
    status, attention, detail, rows, logs = scenario(state, t)
    out = []
    head_l = f" batuta watch · roadmap-20260906-215846 · core · {t['phase']} 2" if width >= 100 else (f" batuta watch · core · {t['phase']} 2" if width >= 76 else ' batuta watch')
    head_r = (f"feat/roadmap @ 4c1e9f2 · {status} " if width >= 76 else f"{status} ")
    out.append(fit(head_l, width - w(head_r)) + head_r)
    pieces = (attention or t['needs_none']).split(' · ')
    while len(pieces) > 1 and w(' ' + ' · '.join(pieces)) > width: pieces.pop()
    out.append(fit(' ' + ' · '.join(pieces), width))
    ctx = ['codex gpt-5.6-sol · medium · go test ./... · workspace-write', f"2 {t['sessions']} · 0 {t['retries']} · 0 {t['escalations']} · PID 99573"]
    prog = [f"{t['waves']}  1/4  " + bar(1, 4, 24) + '  25%', f"{t['tasks']}  1/5  " + bar(1, 5, 24) + '  20%']
    if width >= 100:
        lw = width // 2 - 1; rw = width - lw - 1
        a = box(t['context'], ctx, lw); b = box(t['progress'], prog, rw)
        out += [x + ' ' + y for x, y in zip(a, b)]
        out += box(t['detail'], detail, width)
        cols = [(t['id'], 7), (t['task'], width - 7 - 17 - 7 - 6 - 8 - 2 - 12), (t['status'], 17), (t['attempt'], 7), (t['gates'], 6), (t['commit'], 8)]
    elif width >= 76:
        out += box(t['context'], ctx + prog, width)
        out += box(t['detail'], detail[:3], width)
        cols = [(t['id'], 7), (t['task'], width - 7 - 15 - 5 - 6 - 2 - 8), (t['status'], 15), (t['tr'], 5), (t['gates'], 6)]
    else:
        out += box(t['progress'], prog[:1] + [detail[0]], width)
        cols = [(t['id'], 7), (t['task'], width - 7 - 13 - 6 - 2 - 6), (t['status'], 13), (t['gates'], 6)]
    def row(vals): return ' '.join(fit(v, n) for v, n in zip(vals, [c[1] for c in cols]))
    table = [row([c[0] for c in cols])]
    for id_, title, st, att, gates, commit in rows:
        title = ('  ' if id_.startswith('task') else '') + title
        vals = [id_, title, st, att, gates, commit]
        if width < 100: vals = vals[:5]
        if width < 76: vals = [id_, title, st, gates]
        table.append(row(vals))
    out += box(t['table'], table, width)
    out.append(fit(f" {G['up']} 0 {t['above']} · {G['down']} 0 {t['below']}", width - w(t['keys']) - 1) + t['keys'] + ' ' if width >= 100 else fit(f" {G['up']} 0 · {G['down']} 0 · " + t['keys'].split(' · ')[0] + ' · ? · q', width))
    if width >= 76:
        out += box(t['logs'] + ' · task-2-e1', logs[-(6 if width >= 100 else 3):], width)
    lines = [fit(l.expandtabs(4), width) for l in out]
    if glyphs == 'ascii': lines = [l.replace('·', '-') for l in lines]
    bad = [(i + 1, w(l)) for i, l in enumerate(lines) if w(l) != width]
    return '\n'.join(lines) + '\n', bad

if __name__ == '__main__':
    for state in ('calm', 'question', 'limit', 'blocked', 'conflict'):
        for width in (120, 80, 60):
            for lang, glyphs in (('en', 'unicode'), ('pt', 'unicode'), ('en', 'ascii')):
                if (lang, glyphs) != ('en', 'unicode') and (state != 'calm' or width != 120):
                    continue
                text, bad = render(state, width, lang, glyphs)
                name = f"mock-{state}-{width}" + ('' if (lang, glyphs) == ('en', 'unicode') else f"-{lang}-{glyphs}") + '.txt'
                open(name, 'w').write(text)
                print(name, text.count('\n'), 'lines', 'off-width:' if bad else 'ok', bad if bad else '')
