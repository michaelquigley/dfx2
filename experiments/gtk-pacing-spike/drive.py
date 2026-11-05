#!/usr/bin/env python3
"""Bounded native runs. The probe's control timer is test orchestration only."""
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess

p = argparse.ArgumentParser()
p.add_argument('binary')
p.add_argument('--backend', choices=('wayland', 'x11'), required=True)
p.add_argument('--modes', nargs='+', default=['timer', 'tick'])
p.add_argument('--scale', default='1')
p.add_argument('--out', type=Path, required=True)
a = p.parse_args()
a.out.mkdir(parents=True, exist_ok=True)
results = []
for mode in a.modes:
    env = dict(os.environ, GDK_BACKEND=a.backend, GDK_SCALE=a.scale)
    log = a.out / (mode + '.jsonl')
    with log.open('w') as stdout, (a.out / (mode + '.stderr.log')).open('w') as stderr:
        proc = subprocess.Popen([a.binary, '-mode', mode], env=env, stdout=stdout, stderr=stderr)
        timeout = False
        try:
            code = proc.wait(timeout=35)
        except subprocess.TimeoutExpired:
            timeout = True
            proc.kill()
            code = proc.wait(timeout=5)
    events = [json.loads(line) for line in log.read_text().splitlines()]
    by = {e['event']: e for e in events}
    failures = []
    observations = {}
    def delta(start, end, index, key):
        return by[end]['windows'][index][key] - by[start]['windows'][index][key]
    try:
        for index in range(2):
            observations[f'minimized_window_{index+1}'] = {
                key: delta('minimized-start', 'minimized-end', index, key)
                for key in ('frames', 'paints', 'ticks', 'requests', 'timers', 'samples')}
            observations[f'quiet_window_{index+1}'] = {
                key: delta('quiet-start', 'quiet-end', index, key)
                for key in ('frames', 'ticks', 'requests', 'timers', 'samples')}
        first = observations['minimized_window_1']
        if first['samples'] < 30:
            failures.append('producer did not continue publishing while minimized')
        if mode != 'timer' and any(first[k] for k in ('frames', 'ticks', 'requests', 'timers')):
            failures.append('nonzero minimized frame work: strict suspension not established')
        if mode == 'timer' and first['frames'] == 0:
            failures.append('timer control failed to reproduce continued rendering')
        if observations['minimized_window_2']['frames'] < 20:
            failures.append('other window stopped progressing')
        if delta('minimized-end', 'restored', 0, 'frames') < 1:
            failures.append('restoration did not render')
        restored = by['restored']['windows'][0]
        observations['minimized_interval_seconds'] = by['minimized-end']['seconds'] - by['minimized-start']['seconds']
        observations['restored_sample_lag'] = restored['samples'] - restored['rendered_sample']
        if observations['restored_sample_lag'] > 2:
            failures.append('restored rendering did not catch up to current sample')
        for index in range(2):
            if any(observations[f'quiet_window_{index+1}'].values()):
                failures.append(f'window {index+1} did not become idle')
        if delta('quiet-end', 'exposed', 0, 'frames') != 0:
            failures.append('exposure reevaluated the view')
        if delta('quiet-end', 'exposed', 0, 'paints') < 1:
            failures.append('exposure did not paint retained image')
        if delta('exposed', 'resized', 0, 'frames') < 1:
            failures.append('static resize did not render')
        if mode == 'tick' and by['close-pending-end']['windows'][0]['Tick'] == 0:
            failures.append('close did not exercise a pending tick')
        if delta('close-pending-end', 'survivor', 1, 'frames') < 1:
            failures.append('survivor did not render after other window closed')
        if delta('close-pending-end', 'survivor', 0, 'frames') != 0:
            failures.append('closed window rendered')
        if any(not w['Closed'] or w['Tick'] or w['Timer'] or w['Pending'] for w in by['closed']['windows']):
            failures.append('pending work survived teardown')
    except KeyError as exc:
        failures.append(f'missing event: {exc}')
    if code or timeout:
        failures.append(f'exit={code} timeout={timeout}')
    result = dict(mode=mode, exit=code, timeout=timeout, failures=failures, observations=observations)
    results.append(result)
    print(json.dumps(result), flush=True)
summary = dict(backend=a.backend, scale=a.scale, platform=platform.platform(), results=results)
(a.out / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
raise SystemExit(any(r['failures'] for r in results))
