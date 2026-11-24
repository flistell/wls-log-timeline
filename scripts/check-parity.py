#!/usr/bin/env python3
"""Optional migration check; Python is only needed to run the original oracle."""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import tempfile
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parents[1]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=ROOT / 'wls-events-timeline')
    parser.add_argument('--logs', type=Path, default=ROOT / 'sample-logs')
    args = parser.parse_args()
    with args.binary.open('rb') as binary:
        if binary.read(4) != b'\x7fELF':
            parser.error('--binary must be the compiled Linux executable; run make build first')
    paths = sorted(str(p) for p in args.logs.rglob('*') if p.is_file())
    if not paths:
        parser.error('no sample logs found')
    spec = importlib.util.spec_from_file_location('reference', ROOT / 'testdata/wls-events-timeline.py')
    reference = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(reference)
    with tempfile.TemporaryDirectory(prefix='wls-parity-') as temp:
        for mode, limit in [(None, 12000), ('coherence', 12000), ('coherence', 127)]:
            events, warnings, total = reference.parse_logs(paths, ZoneInfo('Europe/Rome'), limit, detailed_events=mode)
            # The Go report intentionally consolidates this legacy category alias.
            for event in events:
                if event['category'] == 'Work Manager':
                    event['category'] = 'WorkManager'
            output = Path(temp) / 'timeline.html'
            command = [str(args.binary.resolve()), *paths, '--timezone', 'Europe/Rome', '--max-detail-chars', str(limit), '-o', str(output)]
            if mode:
                command += ['--detailed-events', mode]
            subprocess.run(command, check=True, capture_output=True, text=True)
            page = output.read_text()
            actual = json.loads(re.search(r'<script type="application/json" id="data">(.*?)</script>', page, re.S)[1])
            expected = dict(events=events, warnings=warnings, totalLines=total, source=', '.join(paths), zone='Europe/Rome', detailedEvents=mode)
            if actual != expected:
                for key in expected:
                    if actual.get(key) != expected[key]:
                        if key == 'events':
                            print(f'Event counts: Go={len(actual[key])}, Python={len(events)}')
                            for i, (a, b) in enumerate(zip(actual[key], events)):
                                if a != b:
                                    print(f'First different event: {i}')
                                    for field in a.keys() | b.keys():
                                        if a.get(field) != b.get(field):
                                            print(f'  {field}: Go={a.get(field)!r}; Python={b.get(field)!r}')
                                    break
                        else:
                            print(f'Different payload field: {key}')
                raise SystemExit('Parity check failed')
            print(f'PASS: {len(paths)} logs, {len(events)} events, {total} lines, {len(warnings)} warnings; mode={mode or "generic"}, excerpt={limit}')

if __name__ == '__main__':
    main()
