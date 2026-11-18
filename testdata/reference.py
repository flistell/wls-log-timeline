#!/usr/bin/env python3
"""Generate an offline, filterable timeline from multiline WebLogic log records."""
import argparse
import base64
import gzip
from collections import Counter
from datetime import datetime, timedelta, timezone
import html
import json
from pathlib import Path
import re
import sys
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

HEADER = re.compile(r'^(?:####)?<([^>]+)>\s+<(Trace|Debug|Info|Notice|Warning|Error|Critical|Alert|Emergency)>\s+<', re.I)
FIELDS = re.compile(r'<(.*?)>\s+')
CODE = re.compile(r'<([A-Za-z][\w]*-\d{6,}|\d{6})>')
MONTHS = {v: i for i, v in enumerate('Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec'.split(), 1)}
OFFSETS = {'UTC': 0, 'GMT': 0, 'CET': 60, 'CEST': 120, 'EST': -300, 'EDT': -240, 'CST': -360, 'CDT': -300, 'MST': -420, 'MDT': -360, 'PST': -480, 'PDT': -420}
CATEGORY = {
 'BEA-000337': 'Stuck threads', 'BEA-000339': 'Thread recovery',
 'BEA-111015': 'Transaction timeouts', 'BEA-101017': 'HTTP errors',
 'BEA-011139': 'EJB errors', 'BEA-010026': 'EJB errors',
 'BEA-000627': 'JDBC capacity', 'BEA-000628': 'Pool maintenance',
 'BEA-002939': 'Work Manager', 'BEA-002936': 'Work Manager',
}

# Match the event message, not exception/diagnostic text quoted underneath it.
COHERENCE_EVENTS = [
 ('stopping cluster', r'\bstopping cluster(?: service)?\b|\bExiting cluster\b'),
 ('restarting cache', r'\bRestarting NamedCache\s*:'),
 ('Restarting cluster', r'\bRestarting cluster\b'),
 ('Worker STUCK', r'\bA worker thread has been executing task:[^\n]*\bappears to be stuck\b'),
 ('Handshake timeout', r'validatePolls:|unanswered handshake|timeout during service start|failed to restart services'),
 ('Storage unavailable', r'No storage-enabled nodes'),
 ('Guardian timeout', r'Detected (?:soft|hard) timeout|guard(?:ian)?[^\n]*timeout'),
 ('Guardian recovery', r'Attempting recovery of|terminating guarded|recovering guarded'),
 ('Transport ACK timeout', r'initiating connection migration .* ack timeout|fatal ack timeout|ack timeout after'),
 ('Transport migration recovery', r'accepted connection migration|resuming migrated connection|synchronizing migrated connection'),
 ('Transport migration', r'accepting connection migration|migrating connection|rejecting connection migration'),
 ('Transport connection failure', r'Connection reset|ClosedChannelException|SocketTimeoutException|failed to identify protocol|ServerSocket failure|Error (?:connecting|establishing)'),
 ('Connection established', r'\bConnection established with\b'),
 ('Transport diagnostics', r'TEMP DEBUG|post-flush drain|discarding partial message|skipping control message'),
 ('Communication delay', r'communication delay|Failed to satisfy the variance'),
 ('Cluster discovery failure', r'Delaying formation of a new cluster|incompatible network configuration|older member announcing'),
 ('Cluster formation', r'Created a new cluster'),
 ('Member departure', r'left [Cc]luster|MemberLeft notification|removing the member|member .* departed'),
 ('Member join', r'joined [Cc]luster|joined Cluster'),
 ('Partition stabilized', r'Partition ownership has stabilized'),
 ('Partition transfer pending', r'partition distribution has been pending'),
 ('Partition recovery', r'orphaned|orphan partitions|Restored from backup|recover(?:ed|ing) partition'),
 ('Partition transfer', r'Transferring .*PartitionSet|DistributionRequest|[Rr]e-publishing the ownership'),
 ('Service departure', r'left service|Service .* left the cluster'),
 ('Service join', r'joined service|Service .* joined the cluster'),
 ('Service lifecycle', r'Service .* (?:started|stopped)|Starting service|Stopping service'),
 ('Proxy request failure', r'exception occurred while processing .*Request'),
 ('Protocol assertion', r'Assertion failed'),
 ('Management failure', r'Failure to deserialize an Invocable|unsupported JMX service URL'),
]
COHERENCE_EVENTS = [(label, re.compile(pattern, re.I)) for label, pattern in COHERENCE_EVENTS]
BUS_ENDPOINT = re.compile(r'\b(?:tmb|tmbs|tcp|ssl)://(\[[^\]]+\]|[\w.-]+):(\d+)(?:\.(\d+))?')
MEMBER_ENDPOINT = re.compile(r'\bAddress=(\[[^\]]+\]|[\w.-]+):(\d+)')


def coherence_metadata(text, metadata):
    """Accumulate explicit identifiers over the full record, even past excerpt limits."""
    for match in BUS_ENDPOINT.finditer(text):
        metadata['endpoints'].add(f'{match[1]}:{match[2]}')
        metadata['connections'].add(match[0])
    for match in MEMBER_ENDPOINT.finditer(text):
        metadata['endpoints'].add(f'{match[1]}:{match[2]}')
    metadata['services'].update(re.findall(r'\b(?:Service=|ServiceName=|Name=)([\w:$.-]+)', text))
    metadata['services'].update(re.findall(r'\b(?:for service|joined service|left service)\s+["\']?([\w:$.-]+)', text, re.I))
    metadata['services'].update(re.findall(r'\bService\s+([\w:$.-]+)\s+(?:left|joined|started|stopped)', text))
    metadata['clusters'].update(re.findall(r'\bcluster\s+"([^"]+)"', text, re.I))
    # IDs are scoped to a cluster incarnation. They are evidence, never join keys.
    metadata['members'].update(re.findall(r'\bMember\(Id=(\d+)', text))
    metadata['memberNames'].update(re.findall(r'\bmember:([\w.-]+)', text))


def coherence_event(message, metadata):
    body = re.sub(r'^.*?\bmember=(?:\d+|n/a)\):\s*', '', message, count=1)
    event_type = next((label for label, pattern in COHERENCE_EVENTS if pattern.search(body)), 'Other Coherence')
    result = {key: sorted(values) for key, values in metadata.items()}
    member = re.search(r'\bmember=(\d+|n/a)\):', message)
    result['localMember'] = member[1] if member else ''
    engine_thread = re.search(r'\(thread=(.*), member=(?:\d+|n/a)\):', message)
    result['thread'] = engine_thread[1] if engine_thread else ''
    result['eventType'] = event_type
    return result


def timestamp(header, date_text, assume_zone=None):
    """Prefer WebLogic epoch milliseconds; parse textual time only if absent."""
    before_code = header[:CODE.search(header).start()] if CODE.search(header) else header
    epochs = re.findall(r'<(\d{13})>', before_code)
    if epochs:
        return int(epochs[-1])
    try:
        dt = datetime.fromisoformat(date_text.replace('Z', '+00:00'))
    except ValueError:
        m = re.fullmatch(r'(\w{3}) (\d{1,2}), (\d{4}) (\d{1,2}):(\d{2}):(\d{2})(?:[.,](\d{1,6}))? (AM|PM)(?: (\S+))?', date_text, re.I)
        if not m or m[1].title() not in MONTHS:
            raise ValueError('unsupported timestamp: ' + date_text)
        hour = int(m[4])
        if not 1 <= hour <= 12:
            raise ValueError('invalid 12-hour clock: ' + date_text)
        zone = m[9]
        if zone and zone.upper() in OFFSETS:
            tz = timezone(timedelta(minutes=OFFSETS[zone.upper()]))
        elif zone and re.fullmatch(r'[+-]\d{2}:?\d{2}', zone):
            z = zone.replace(':', ''); tz = timezone(timedelta(minutes=(int(z[1:3])*60+int(z[3:]))*(1 if z[0]=='+' else -1)))
        elif assume_zone is not None:
            tz = assume_zone
        else:
            raise ValueError('unknown or missing timezone; use --assume-timezone: ' + date_text)
        dt = datetime(int(m[3]), MONTHS[m[1].title()], int(m[2]), hour%12+(12 if m[8].upper()=='PM' else 0), int(m[5]), int(m[6]), int((m[7] or '0').ljust(6, '0')), tzinfo=tz)
    if dt.tzinfo is None:
        if assume_zone is None:
            raise ValueError('timestamp has no timezone; use --assume-timezone: ' + date_text)
        dt = dt.replace(tzinfo=assume_zone)
    return round(dt.timestamp()*1000)


def parse_log(path, display_zone, max_detail_chars=12000, assume_zone=None, detailed_events=None):
    events, warnings = [], []
    current = None
    preamble = 0
    total_lines = 0

    def finish(record):
        if record is None:
            return
        h = record['header']; fields = FIELDS.findall(h)
        try:
            epoch = timestamp(h, HEADER.match(h)[1], assume_zone)
            dt = datetime.fromtimestamp(epoch/1000, display_zone)
        except (ValueError, OverflowError, OSError) as exc:
            warnings.append(f"Line {record['line']}: {exc}")
            return
        match = CODE.search(h)
        code = match[1] if match else 'NO-CODE'
        message = h[match.end():].strip() if match else h
        if message.startswith('<'): message = message[1:]
        message = message.rstrip(' >')
        subsystem = fields[2] if len(fields)>2 else 'Unknown'
        severity = HEADER.match(h)[2].capitalize()
        category = CATEGORY.get(code, subsystem or 'Other')
        if subsystem.casefold() == 'weblogicserver' and re.search(r'\bServer state changed to\b', message, re.I):
            category = 'WebLogic Server Lifecycle'
        # Header fields before the nested user identity have stable positions.
        host = fields[3] if len(fields)>3 else ''
        server = fields[4] if len(fields)>4 else ''
        thread = fields[5] if len(fields)>5 else ''
        if not h.startswith('####'):
            host = server = thread = ''
        xids = sorted(record['xids'])
        events.append(dict(id='L'+str(record['line']), epoch=epoch, date=dt.strftime('%Y-%m-%d'), time=dt.strftime('%H:%M:%S.%f')[:-3], originalTime=HEADER.match(h)[1], severity=severity, category=category, code=code, subsystem=subsystem, host=host, server=server, thread=thread, line=record['line'], endline=record['endline'], summary=message[:500], detail=record['detail'], truncated=record['chars']>len(record['detail']), originalChars=record['chars'], xids=xids))
        if record['coherence'] is not None:
            info = coherence_event(message, record['coherence'])
            events[-1].update(coherence=info, eventType=info['eventType'], category='Coherence: '+info['eventType'])

    opener = gzip.open if Path(path).suffix == '.gz' else open
    with opener(path, 'rt', encoding='utf-8', errors='replace') as stream:
        for total_lines, line in enumerate(stream, 1):
            if HEADER.match(line):
                finish(current)
                current = dict(header=line.rstrip('\r\n'), line=total_lines, endline=total_lines, detail='', chars=0, xids=set())
                fields = FIELDS.findall(line)
                is_coherence = detailed_events == 'coherence' and len(fields)>2 and 'coherence' in fields[2].lower()
                current['coherence'] = {key: set() for key in ('endpoints', 'connections', 'services', 'clusters', 'members', 'memberNames')} if is_coherence else None
            if current is None:
                preamble += 1
                continue
            current['endline'] = total_lines
            current['chars'] += len(line)
            room = max_detail_chars-len(current['detail'])
            if room>0: current['detail'] += line[:room]
            current['xids'].update(re.findall(r'\bBEA1-[A-Za-z0-9]+', line))
            if current['coherence'] is not None:
                coherence_metadata(line, current['coherence'])
    finish(current)
    events.sort(key=lambda e:(e['epoch'],e['line']))
    if preamble:
        warnings.insert(0, f'{preamble} preamble line(s) before the first recognized record were not plotted.')
    return events, warnings, total_lines


def parse_logs(paths, display_zone, max_detail_chars=12000, assume_zone=None, detailed_events=None):
    """Merge independent logs, retaining source locations and unique event IDs."""
    events, warnings, total_lines = [], [], 0
    for index, path in enumerate(paths, 1):
        source = str(path)
        file_events, file_warnings, lines = parse_log(path, display_zone, max_detail_chars, assume_zone, detailed_events)
        for event in file_events:
            event['id'] = f"F{index}-{event['id']}"
            event['source'] = source
        events.extend(file_events)
        warnings.extend(f'{source}: {warning}' for warning in file_warnings)
        total_lines += lines
    # Stable sorting preserves input-file order, then line order, for equal times.
    events.sort(key=lambda event: event['epoch'])
    return events, warnings, total_lines


def make_html(events, source, zone, warnings, title, total_lines, detailed_events=None, stylesheet=None):
    payload = json.dumps(dict(events=events, source=source, zone=zone, warnings=warnings, totalLines=total_lines, detailedEvents=detailed_events), ensure_ascii=False).replace('<', '\\u003c').replace('\u2028','\\u2028').replace('\u2029','\\u2029')
    # A data URL keeps CSS offline and prevents CSS text from closing an HTML style tag.
    custom_style = ''
    if stylesheet is not None:
        encoded = base64.b64encode(stylesheet.encode('utf-8')).decode('ascii')
        custom_style = f'<link id="custom-stylesheet" rel="stylesheet" href="data:text/css;charset=utf-8;base64,{encoded}">'
    return TEMPLATE.replace('__STYLESHEET__',custom_style).replace('__TITLE__',html.escape(title)).replace('__PAYLOAD__',payload)


TEMPLATE = r'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>__TITLE__</title>
<style>
:root{color-scheme:dark;--bg:#0b1220;--panel:#142034;--line:#354b69;--muted:#b7c8df;--accent:#87d5ff}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:#eff5ff;font:15px/1.5 system-ui,sans-serif}main{width:100%;max-width:none;margin:0;padding:24px}h1{margin:0;font-size:28px}h2{font-size:19px}.card{width:100%;min-width:0;background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:18px;margin:16px 0}small,.muted{color:var(--muted)}input,select,button{font:inherit;background:#0b1729;color:#eff5ff;border:1px solid #60799c;border-radius:6px;padding:7px}button{cursor:pointer}button:hover,button:focus-visible{outline:2px solid var(--accent)}.controls{display:flex;gap:12px;flex-wrap:wrap;align-items:end}label.field{display:flex;flex-direction:column;gap:4px}input[type=checkbox]{accent-color:#68bbff}#search{min-width:260px}.filter-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(230px,1fr));gap:12px}details{border:1px solid var(--line);border-radius:8px;padding:10px}summary{cursor:pointer;font-weight:600}.options{display:flex;flex-wrap:wrap;gap:7px;margin-top:8px;max-height:180px;overflow:auto}.options label{border:1px solid var(--line);border-radius:16px;padding:3px 8px;font-size:13px}.mini{font-size:12px;padding:3px 8px;margin:8px 4px 0 0}.layout{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.2fr);gap:16px;align-items:start}#list{max-height:620px;overflow:auto}.row{display:flex;gap:9px;margin:6px 0;padding:8px;background:#0d192b;border-left:4px solid var(--color);border-radius:6px;align-items:start}.row.off{opacity:.55}.row button{flex:1;min-width:0;overflow-wrap:anywhere;text-align:left;border:0;background:transparent;padding:0}.row.selected{background:#284567}.row input{margin-top:6px}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#081221;font:12px/1.5 ui-monospace,monospace;padding:12px;max-height:430px;overflow:auto}#detail{position:static;align-self:start;max-height:none;overflow:auto;overflow-wrap:anywhere}.badge{display:inline-block;border:1px solid #6e88aa;border-radius:12px;padding:2px 8px;font-size:12px;margin:3px}#plot{overflow:auto}#chart{width:100%;min-width:900px}svg text{fill:#d9e6f8;font:12px system-ui}svg .mark{cursor:pointer}svg .mark:focus{outline:2px solid white}#warning{color:#ffd58b}.paging{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-top:10px}@media(max-width:850px){main{padding:12px}.layout{display:block}#detail{position:static;max-height:none}}@media print{.controls,.filter-grid,.paging{display:none}.layout{display:block}#detail{position:static;max-height:none}}

#filterCard{position:sticky;top:8px;z-index:100;padding:12px 16px;max-height:50vh;overflow:auto;box-shadow:0 8px 24px #0008}
#filterCard .controls{gap:8px}
#filterCard input{max-width:100%}
#filterCard .field{min-width:0}
.filter-panel{margin-top:10px}
.filter-panel>summary{font-size:14px}
@media print{#filterCard{position:static;max-height:none;overflow:visible;box-shadow:none}}
:root{--severity-trace:#94a3b8;--severity-debug:#c4b5fd;--severity-info:#7dd3fc;--severity-notice:#86efac;--severity-warning:#fbbf24;--severity-error:#f87171;--severity-critical:#fb7185;--severity-alert:#f472b6;--severity-emergency:#ff3b5c}
[data-severity="Trace"]{--event-accent:var(--severity-trace)}
[data-severity="Debug"]{--event-accent:var(--severity-debug)}
[data-severity="Info"]{--event-accent:var(--severity-info)}
[data-severity="Notice"]{--event-accent:var(--severity-notice)}
[data-severity="Warning"]{--event-accent:var(--severity-warning)}
[data-severity="Error"]{--event-accent:var(--severity-error)}
[data-severity="Critical"]{--event-accent:var(--severity-critical)}
[data-severity="Alert"]{--event-accent:var(--severity-alert)}
[data-severity="Emergency"]{--event-accent:var(--severity-emergency)}
.row[data-severity]{border-left-color:var(--event-accent)}
.mark[data-severity] circle{fill:var(--event-accent)}
.badge[data-severity],.options label[data-severity]{color:var(--event-accent);border-color:var(--event-accent)}
#exclude{min-width:280px}
.filter-error{color:var(--severity-error);margin:8px 0 0}
button:disabled{opacity:.5;cursor:default}
#chart{touch-action:pan-y;user-select:none;cursor:crosshair}
.chart-selection{pointer-events:none;fill:var(--accent);fill-opacity:.18;stroke:var(--accent);stroke-width:1}
</style>__STYLESHEET__</head><body><main><h1>__TITLE__</h1><p id="scope" class="muted"></p><p id="warning"></p>
<section id="filterCard" class="card" aria-label="Timeline filters"><div class="controls"><label class="field">From<input id="from" type="text" placeholder="DD/MM/YYYY HH:mm:ss.SSS" title="DD/MM/YYYY HH:mm:ss.SSS (24-hour time); seconds and milliseconds optional" size="26" autocomplete="off"></label><label class="field">To<input id="to" type="text" placeholder="DD/MM/YYYY HH:mm:ss.SSS" title="DD/MM/YYYY HH:mm:ss.SSS (24-hour time); seconds and milliseconds optional" size="26" autocomplete="off"></label><span id="filterZone" class="muted"></span><label class="field">Search<input id="search" type="search" placeholder="Message, Xid, thread, code…"></label><label class="field">Exclude (regex)<input id="exclude" type="search" placeholder="TEMP DEBUG|post-flush, Connection reset" title="Exclude records matching any comma-separated regex or | alternative (case insensitive). Searches retained record text, including stack traces." aria-describedby="excludeError"></label><button id="reset">Reset filters</button><button id="export">Export plotted events</button><button id="zoomIn" title="Halve the current time range (+)">Zoom in</button><button id="zoomOut" title="Double the current time range (-)">Zoom out</button><button id="zoomReset" title="Restore the full time range (Home)">Full range</button></div><p id="excludeError" class="filter-error" role="alert" hidden></p><details class="filter-panel"><summary>Filter options</summary><p class="muted">Toggle categories, severity levels, message codes, servers, hostnames or source files. Individual event checkboxes below control whether those events appear on the chart. Search includes retained log detail and all extracted Xids.</p><div id="filters" class="filter-grid"></div></details></section>
<section class="card"><p id="count" aria-live="polite"></p><p class="muted">Each dot is a log record at its recorded time. Stuck/timeout durations are not drawn as inferred activity intervals. Click a dot or event for evidence; narrow the time window to separate dense clusters.</p><p id="chartHelp" class="muted">Drag horizontally across the plot to select and zoom into a time range across all lanes. Use Zoom in / Zoom out or Full range above. With the chart focused: + / - to zoom, Home for full range, Esc to cancel selection.</p><div id="plot"><svg id="chart" role="img" tabindex="0" aria-describedby="chartHelp" aria-label="Interactive WebLogic event timeline"></svg></div></section>
<div class="layout"><section class="card"><h2>Events</h2><div class="controls"><button id="showEvents">Show matching events</button><button id="hideEvents">Hide matching events</button><button id="restore">Restore all hidden events</button></div><p class="muted">These controls apply to every matching event, including other pages. Filtered-out records remain available when filters are restored.</p><div id="list"></div><div class="paging"><button id="prev">Previous</button><span id="page"></span><button id="next">Next</button></div></section><section id="detail" class="card" aria-live="polite"><h2>Event details</h2><p>Select an event.</p></section></div>
<p class="muted">Self-contained HTML; no network requests, external libraries or log modifications. The report embeds log excerpts and should be handled with the same access controls as the source log. Counters represent records, not unique failed requests.</p>
<script type="application/json" id="data">__PAYLOAD__</script><script>
'use strict';
const D=JSON.parse(document.getElementById('data').textContent), E=D.events, $=id=>document.getElementById(id);
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const keys=['category','severity','code','server','host','source'], names={category:'Categories',severity:'Severity',code:'Message codes',server:'Servers',host:'Hostnames',source:'Source files',eventType:'Coherence event types',services:'Coherence services',clusters:'Explicit cluster names',endpoints:'Member endpoints'}, selected={}, hidden=new Set(), indexed=new Map(E.map(e=>[e.id,e]));
if(D.detailedEvents==='coherence')keys.push('eventType','services','clusters','endpoints');
const valuesOf=(e,k)=>{const v=e[k]??e.coherence?.[k]??'';return Array.isArray(v)?(v.length?v:['']):[v]};
const categories=[...new Set(E.map(e=>e.category))].sort(), colors=new Map(categories.map((c,i)=>[c,`hsl(${(i*137.508)%360} 76% 72%)`]));
let matching=[],plotted=[],page=0,active=null,trackedEndpoint='';const pageSize=100;
const laneControl=document.createElement('label');laneControl.className='field';laneControl.textContent='Group chart by';
const laneSelect=document.createElement('select');laneSelect.id='laneGroup';
for(const key of ['category','host','server']){const opt=document.createElement('option');opt.value=key;opt.textContent=names[key];laneSelect.append(opt)}
laneControl.append(laneSelect);$('reset').parentElement.append(laneControl);
const trackStatus=document.createElement('span');trackStatus.id='trackStatus';$('reset').parentElement.append(trackStatus);
const clearTrack=document.createElement('button');clearTrack.textContent='Clear endpoint tracking';clearTrack.hidden=true;$('reset').parentElement.append(clearTrack);
const overview=document.createElement('p');overview.className='muted';$('plot').before(overview);
laneSelect.onchange=()=>render();clearTrack.onclick=()=>{trackedEndpoint='';page=0;render()};
$('scope').textContent=`${D.source} · ${E.length.toLocaleString()} records · ${D.totalLines.toLocaleString()} source lines · display timezone: ${D.zone}. All dates and time filters use this timezone.`;
$('warning').textContent=D.warnings.length?`${D.warnings.length} parsing warning(s): ${D.warnings.slice(0,8).join(' | ')}${D.warnings.length>8?' | Additional warnings were printed by the generator.':''}`:'';
$('filterZone').textContent=`Timezone: ${D.zone}`;
keys.forEach(key=>{const counts=new Map();E.forEach(e=>new Set(valuesOf(e,key)).forEach(v=>counts.set(v,(counts.get(v)||0)+1)));selected[key]=new Set(counts.keys());const box=document.createElement('details');box.open=key==='category';const summary=document.createElement('summary');summary.textContent=names[key];box.append(summary);
 for(const [label,checked] of [['All',true],['None',false]]){const b=document.createElement('button');b.className='mini';b.textContent=label;b.onclick=()=>{selected[key]=new Set(checked?counts.keys():[]);box.querySelectorAll('input').forEach(i=>i.checked=checked);page=0;render()};box.append(b)}
 const options=document.createElement('div');options.className='options';[...counts].sort((a,b)=>a[0].localeCompare(b[0])).forEach(([value,count])=>{const label=document.createElement('label'),input=document.createElement('input');input.type='checkbox';input.checked=true;input.dataset.key=key;input.value=value;input.onchange=()=>{if(input.checked)selected[key].add(value);else selected[key].delete(value);page=0;render()};if(key==='severity')label.dataset.severity=value;label.append(input,document.createTextNode(` ${value||'(unspecified)'} (${count})`));options.append(label)});box.append(options);$('filters').append(box)});
// Use UTC only as a numeric calendar scale: values already use D.zone.
// Parsing without the Z would incorrectly apply the browser's timezone.
const calendar=s=>Date.parse(s+'Z');
const eventTime=e=>calendar(e.date+'T'+e.time);
const filterText=n=>{const s=new Date(n).toISOString();return `${s.slice(8,10)}/${s.slice(5,7)}/${s.slice(0,4)} ${s.slice(11,23)}`};
function filterTime(value){
 const m=/^(\d{2})\/(\d{2})\/(\d{4}) (\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/.exec(value.trim());
 if(!m)return NaN;
 const iso=`${m[3]}-${m[2]}-${m[1]}T${m[4]}:${m[5]}:${m[6]||'00'}.${(m[7]||'').padEnd(3,'0')}`;
 const n=calendar(iso);
 // Reject rollover dates and times such as 31/02 or 24:00.
 return Number.isFinite(n)&&new Date(n).toISOString().slice(0,23)===iso?n:NaN;
}
const clock=n=>new Date(Math.round(n)).toISOString().slice(0,19).replace('T',' ');
const rangeStart=E.reduce((n,e)=>Math.min(n,eventTime(e)),Infinity),rangeEnd=E.reduce((n,e)=>Math.max(n,eventTime(e)),-Infinity);
let chartWindow=null,dragSelection=null,suppressChartClick=false;
function currentWindow(){return {lo:$('from').value?filterTime($('from').value):rangeStart,hi:$('to').value?filterTime($('to').value):rangeEnd}}
function setTimeWindow(lo,hi){
 if(!Number.isFinite(lo)||!Number.isFinite(hi)||hi<lo)return;
 const span=Math.min(Math.max(1,Math.round(hi-lo)),rangeEnd-rangeStart);
 lo=Math.round(Math.max(rangeStart,Math.min(lo,rangeEnd-span)));hi=lo+span;
 $('from').value=filterText(lo);$('to').value=filterText(hi);page=0;render();
}
function zoomWindow(factor){
 const {lo,hi}=currentWindow();if(!Number.isFinite(lo)||!Number.isFinite(hi)||hi<lo)return;
 const center=lo+(hi-lo)/2,span=Math.max(1,hi-lo)*factor;
 setTimeWindow(center-span/2,center+span/2);
}
function chartPosition(event){
 const matrix=$('chart').getScreenCTM();if(!matrix)return null;
 const point=$('chart').createSVGPoint();point.x=event.clientX;point.y=event.clientY;
 return point.matrixTransform(matrix.inverse());
}
const plotX=x=>Math.max(190,Math.min(1170,x));
const timeAtX=(x,window)=>window.lo+(plotX(x)-190)/980*(window.hi-window.lo);
function cancelSelection(){
 const drag=dragSelection;dragSelection=null;
 if(drag?.rect)drag.rect.remove();
 if(drag&&$('chart').hasPointerCapture(drag.pointerId))$('chart').releasePointerCapture(drag.pointerId);
}
function beginSelection(event){
 if(!event.isPrimary||event.button!==0||!chartWindow||chartWindow.hi<=chartWindow.lo)return;
 const point=chartPosition(event);
 if(!point||point.x<190||point.x>1170||point.y<38||point.y>chartWindow.height)return;
 cancelSelection();suppressChartClick=false;
 dragSelection={pointerId:event.pointerId,start:point.x,clientStart:event.clientX,moved:false,window:{...chartWindow}};
}
function moveSelection(event){
 const drag=dragSelection;if(!drag||event.pointerId!==drag.pointerId)return;
 const point=chartPosition(event);if(!point)return;
 if(!drag.moved&&Math.abs(event.clientX-drag.clientStart)<4)return;
 if(!drag.moved){
  drag.moved=true;$('chart').setPointerCapture(event.pointerId);
  drag.rect=document.createElementNS('http://www.w3.org/2000/svg','rect');drag.rect.setAttribute('class','chart-selection');
  drag.rect.setAttribute('y','38');drag.rect.setAttribute('height',String(drag.window.height-48));$('chart').append(drag.rect);
 }
 event.preventDefault();
 drag.end=plotX(point.x);drag.rect.setAttribute('x',String(Math.min(drag.start,drag.end)));drag.rect.setAttribute('width',String(Math.abs(drag.end-drag.start)));
}
function finishSelection(event){
 const drag=dragSelection;if(!drag||event.pointerId!==drag.pointerId)return;
 const point=chartPosition(event);cancelSelection();
 if(!drag.moved||!point||Math.abs(plotX(point.x)-drag.start)<1)return;
 event.preventDefault();suppressChartClick=true;
 const a=timeAtX(drag.start,drag.window),b=timeAtX(point.x,drag.window);
 setTimeWindow(Math.min(a,b),Math.max(a,b));
}
$('chart').onpointerdown=beginSelection;$('chart').onpointermove=moveSelection;$('chart').onpointerup=finishSelection;
$('chart').onpointercancel=cancelSelection;
$('chart').onlostpointercapture=cancelSelection;
$('chart').onpointerleave=()=>{if(dragSelection&&!dragSelection.moved)cancelSelection()};
$('chart').addEventListener('click',event=>{if(suppressChartClick){event.preventDefault();event.stopPropagation();suppressChartClick=false}},true);
$('chart').onkeydown=event=>{
 if(event.key==='Escape'){cancelSelection();event.preventDefault()}
 else if(event.key==='+'||event.key==='='){zoomWindow(.5);event.preventDefault()}
 else if(event.key==='-'){zoomWindow(2);event.preventDefault()}
 else if(event.key==='Home'){fullRange();event.preventDefault()}
};
$('zoomIn').onclick=()=>zoomWindow(.5);$('zoomOut').onclick=()=>zoomWindow(2);$('zoomReset').onclick=()=>fullRange();
function choose(id){
 active=id;const e=indexed.get(id),c=e.coherence;
 const meta=c?`<p><strong>${esc(c.eventType)}</strong><br>Local member: ${esc(c.localMember||'(unspecified)')} · Coherence thread: ${esc(c.thread)}<br>Services: ${esc(c.services.join(', '))}<br>Explicit cluster names: ${esc(c.clusters.join(', '))}<br>Referenced members: ${esc(c.members.join(', '))} · ${esc(c.memberNames.join(', '))}</p><p>Track a member endpoint across hosts:</p><div id="trackEndpoints"></div><p class="muted">Endpoint matches show related evidence, not proven causality. Endpoint tracking spans restarts; connection addresses below retain incarnation suffixes. Member numbers are cluster-incarnation scoped.</p><pre>${esc(c.connections.join('\n'))}</pre>`:'';
 $('detail').innerHTML=`<h2>Event details</h2><pre>${esc(e.detail)}</pre>${e.truncated?'<p class="badge">Excerpt truncated; original record '+e.originalChars+' characters. Read the source log for the complete record.</p>':''}<h3>Extracted details</h3><p><strong>${esc(e.code)} · ${esc(e.subsystem)}</strong></p><p><span class="badge" data-severity="${esc(e.severity)}">${esc(e.severity)}</span><span class="badge">${esc(e.category)}</span></p><p><strong>${esc(e.date)} ${esc(e.time)} ${esc(D.zone)}</strong></p><p class="muted">Original timestamp: ${esc(e.originalTime)}<br>Source: ${esc(e.source)} · lines ${e.line}–${e.endline}<br>Server: ${esc(e.server)} · host: ${esc(e.host)}<br>Thread: ${esc(e.thread)}</p><p>${esc(e.summary)}</p>${meta}${e.xids.length?'<p>Xid: '+e.xids.map(esc).join(', ')+'</p>':''}`;
 if(c)for(const endpoint of c.endpoints){const button=document.createElement('button');button.className='mini';button.textContent=endpoint;button.onclick=()=>{trackedEndpoint=endpoint;page=0;render()};$('trackEndpoints').append(button)}
 document.querySelectorAll('.row').forEach(r=>r.classList.toggle('selected',r.dataset.id===id));
}
function compileExclusions(value){
 // Commas inside groups, character classes, quantifiers or escapes are regex content.
 const parts=[];let part='',escaped=false,inClass=false,depth=0;
 for(let i=0;i<value.length;i++){
  const ch=value[i];
  if(escaped){part+=ch;escaped=false;continue}
  if(ch==='\\'){part+=ch;escaped=true;continue}
  if(inClass){part+=ch;if(ch===']')inClass=false;continue}
  if(ch==='['){part+=ch;inClass=true;continue}
  if(ch==='(')depth++;
  if(ch===')')depth=Math.max(0,depth-1);
  if(ch==='{'){const quantifier=/^\{\d+(?:,\d*)?\}/.exec(value.slice(i));if(quantifier){part+=quantifier[0];i+=quantifier[0].length-1;continue}}
  if(ch===','&&depth===0){if(part.trim())parts.push(part.trim());part=''}else part+=ch;
 }
 if(part.trim())parts.push(part.trim());
 try{return {patterns:parts.map(pattern=>new RegExp(pattern,'im')),error:''}}
 catch(error){return {patterns:[],error:'Invalid exclusion regex: '+error.message}}
}
function render(){const lo=$('from').value?filterTime($('from').value):rangeStart,hi=$('to').value?filterTime($('to').value):rangeEnd,query=$('search').value.toLowerCase(),exclusion=compileExclusions($('exclude').value);
 cancelSelection();
 const validRange=Number.isFinite(lo)&&Number.isFinite(hi)&&hi>=lo;
 $('zoomIn').disabled=!validRange||hi-lo<=1;$('zoomOut').disabled=!validRange||(lo<=rangeStart&&hi>=rangeEnd);
 $('excludeError').textContent=exclusion.error;$('excludeError').hidden=!exclusion.error;$('exclude').setAttribute('aria-invalid',String(Boolean(exclusion.error)));$('export').disabled=Boolean(exclusion.error);
 const candidates=E.filter(e=>eventTime(e)>=lo&&eventTime(e)<=hi&&keys.every(k=>valuesOf(e,k).some(v=>selected[k].has(v)))&&(!trackedEndpoint||e.coherence?.endpoints.includes(trackedEndpoint))&&(!query||e.search.includes(query)));
 matching=exclusion.error?[]:candidates.filter(e=>!exclusion.patterns.some(pattern=>pattern.test(e.detail)));plotted=matching.filter(e=>!hidden.has(e.id));
 trackStatus.textContent=trackedEndpoint?'Tracking endpoint '+trackedEndpoint+' across source hosts':'';clearTrack.hidden=!trackedEndpoint;
 $('count').textContent=exclusion.error?exclusion.error:(!Number.isFinite(lo)||!Number.isFinite(hi))?'Invalid date/time: use DD/MM/YYYY HH:mm:ss.SSS (24-hour time).':hi<lo?'Invalid time window: end precedes start.':`${plotted.length.toLocaleString()} plotted · ${matching.length.toLocaleString()} matching · ${(candidates.length-matching.length).toLocaleString()} excluded by regex · ${hidden.size.toLocaleString()} individually hidden across all dates · ${E.length.toLocaleString()} total records`;
 if(active&&!matching.some(e=>e.id===active)){active=null;$('detail').innerHTML='<h2>Event details</h2><p>Select an event.</p>'}
 const laneKey=laneSelect.value||'category',laneOf=e=>e[laneKey]||'(unspecified)',lanes=[...new Set(plotted.map(laneOf))].sort(),height=Math.max(150,lanes.length*46+60),x=n=>190+(n-lo)/Math.max(1,hi-lo)*980;let svg=`<rect width="1200" height="${height}" fill="#142034"/>`;
 chartWindow=validRange?{lo,hi,height}:null;
 if(hi>=lo)for(let i=0;i<=6;i++){const n=lo+(hi-lo)*i/6,xx=x(n);svg+=`<line x1="${xx}" x2="${xx}" y1="28" y2="${height-10}" stroke="#354b69"/><text x="${xx}" y="18" text-anchor="middle">${clock(n).slice(0,10)}<tspan x="${xx}" dy="14">${clock(n).slice(11)}</tspan></text>`}
 // Dense views aggregate adjacent timestamps per lane; exports and lists retain all records.
 const dense=plotted.length>10000;
 overview.textContent=dense?'Dense chart: dots group records into time buckets per lane. Click a group to zoom; lists and exports retain every matching record.':'';
 const byLane=new Map(lanes.map(c=>[c,[]]));plotted.forEach(e=>byLane.get(laneOf(e)).push(e));
 lanes.forEach((c,i)=>{const y=55+i*46;svg+=`<text x="8" y="${y+4}"><title>${esc(c)}</title>${esc(c.slice(0,25))}</text><line x1="190" x2="1170" y1="${y}" y2="${y}" stroke="#354b69"/>`;
  const buckets=new Map();byLane.get(c).forEach(e=>{const key=dense?`${Math.floor((eventTime(e)-lo)/Math.max(1,hi-lo)*200)}:${e.severity}`:e.id;const bucket=buckets.get(key);if(bucket){bucket.count++;bucket.end=eventTime(e)}else buckets.set(key,{event:e,count:1,start:eventTime(e),end:eventTime(e)})});
  [...buckets.values()].forEach((b,j)=>{const e=b.event,label=b.count>1?`${b.count} ${e.severity} records · ${c} · click to zoom`:`${e.date} ${e.time} ${e.code} ${e.summary}`;svg+=`<g class="mark" data-severity="${esc(e.severity)}" data-id="${e.id}" data-count="${b.count}" data-start="${b.start}" data-end="${b.end}" tabindex="0" role="button" aria-label="${esc(label)}"><title>${esc(label)}</title><circle cx="${x(b.start)}" cy="${y+(j%5-2)*5}" r="${b.count>1?7:5}" fill="${colors.get(e.category)}"/></g>`});
 });
 if(!plotted.length)svg+='<text x="200" y="75">No plotted events. Adjust filters or restore hidden events.</text>';
 $('chart').setAttribute('viewBox',`0 0 1200 ${height}`);$('chart').innerHTML=svg;
 $('chart').querySelectorAll('.mark').forEach(g=>{const pick=()=>{if(Number(g.dataset.count)>1){$('from').value=filterText(Number(g.dataset.start));$('to').value=filterText(Number(g.dataset.end));page=0;render();choose(g.dataset.id)}else choose(g.dataset.id)};g.onclick=pick;g.onkeydown=e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();pick()}}});
 const pages=Math.max(1,Math.ceil(matching.length/pageSize));page=Math.max(0,Math.min(page,pages-1));$('list').innerHTML=matching.slice(page*pageSize,(page+1)*pageSize).map(e=>`<div class="row ${hidden.has(e.id)?'off':''} ${active===e.id?'selected':''}" data-id="${e.id}" data-severity="${esc(e.severity)}" style="--color:${colors.get(e.category)}"><input type="checkbox" aria-label="Plot event ${e.id}" ${hidden.has(e.id)?'':'checked'}><button type="button"><small>${esc(e.date)} ${esc(e.time)} · ${esc(e.severity)} · ${esc(e.source)} · line ${e.line}</small><br>${esc(e.code)} · ${esc(e.category)} · ${esc(e.summary.slice(0,220))}<br><small>${esc(e.server)} · ${esc(e.thread)}</small></button></div>`).join('')||'<p>No matching events.</p>';
 $('list').querySelectorAll('.row').forEach(row=>{row.querySelector('button').onclick=()=>choose(row.dataset.id);row.querySelector('input').onchange=e=>{if(e.target.checked)hidden.delete(row.dataset.id);else hidden.add(row.dataset.id);render()}});
 $('page').textContent=`Page ${page+1} / ${pages}`;$('prev').disabled=page===0;$('next').disabled=page>=pages-1;
}
E.forEach(e=>e.search=[e.summary,e.detail,e.code,e.severity,e.subsystem,e.category,e.server,e.host,e.thread,e.source,...e.xids,JSON.stringify(e.coherence??{})].join(' ').toLowerCase());
function fullRange(){$('from').value=filterText(rangeStart);$('to').value=filterText(rangeEnd);page=0;render()}
['from','to','search','exclude'].forEach(id=>$(id).oninput=()=>{page=0;render()});$('prev').onclick=()=>{page--;render()};$('next').onclick=()=>{page++;render()};
$('showEvents').onclick=()=>{matching.forEach(e=>hidden.delete(e.id));render()};$('hideEvents').onclick=()=>{matching.forEach(e=>hidden.add(e.id));render()};$('restore').onclick=()=>{hidden.clear();render()};
$('reset').onclick=()=>{hidden.clear();trackedEndpoint='';$('search').value='';$('exclude').value='';keys.forEach(k=>selected[k]=new Set(E.flatMap(e=>valuesOf(e,k))));$('filters').querySelectorAll('input').forEach(i=>i.checked=true);fullRange()};
$('export').onclick=()=>{const data=plotted.map(({search,...e})=>e),url=URL.createObjectURL(new Blob([JSON.stringify({source:D.source,timezone:D.zone,events:data},null,2)],{type:'application/json'})),a=document.createElement('a');a.href=url;a.download='weblogic-events.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000)};fullRange();
</script></main></body></html>'''


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__, formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    parser.add_argument('log',type=Path,nargs='+',help='WebLogic server or stdout logs (multiline records), merged by timestamp')
    parser.add_argument('-o','--output',type=Path,help='HTML destination; default: weblogic-timeline.html in the current directory')
    parser.add_argument('--title',default='WebLogic event timeline')
    parser.add_argument('--timezone',default='UTC',help='IANA timezone for display and date/time filters')
    parser.add_argument('--assume-timezone',help='IANA timezone for textual timestamps without a recognized zone; epoch timestamps take precedence')
    parser.add_argument('--max-detail-chars',type=int,default=12000,help='Maximum embedded excerpt per record; all records and Xids are retained')
    parser.add_argument('--detailed-events', choices=['coherence'], help='Classify Coherence events and track explicit endpoints/services across hosts')
    parser.add_argument('--stylesheet', type=Path, help='Custom CSS file, embedded after the default styles for offline appearance overrides')
    args=parser.parse_args(argv)
    if args.max_detail_chars<1: parser.error('--max-detail-chars must be positive')
    try:
        zone=ZoneInfo(args.timezone); assume=ZoneInfo(args.assume_timezone) if args.assume_timezone else None
    except ZoneInfoNotFoundError as exc: parser.error(f'Unknown timezone: {exc}')
    output=args.output or Path('weblogic-timeline.html')
    if any(output.resolve()==path.resolve() for path in args.log): parser.error('output must differ from every input log')
    if args.stylesheet and output.resolve()==args.stylesheet.resolve(): parser.error('output must differ from the stylesheet')
    try:
        stylesheet = args.stylesheet.read_text(encoding='utf-8-sig') if args.stylesheet else None
        events,warnings,total=parse_logs(args.log,zone,args.max_detail_chars,assume,args.detailed_events)
        for warning in warnings: print('Warning: '+warning,file=sys.stderr)
        if not events: parser.error('no timestamped WebLogic records recognized; no output written')
        output.write_text(make_html(events,', '.join(str(path) for path in args.log),args.timezone,warnings,args.title,total,args.detailed_events,stylesheet),encoding='utf-8')
    except (OSError,ValueError) as exc: parser.exit(1,f'Error: {exc}\n')
    print(f'Created {output}: {len(events)} records, {len(warnings)} parsing warnings; timezone {args.timezone}.')
    return 0


if __name__=='__main__':
    sys.exit(main())
