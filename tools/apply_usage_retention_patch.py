#!/usr/bin/env python3
from pathlib import Path
import re

root = Path(__file__).resolve().parents[1]
stats_path = root / 'internal/gateway/stats.go'
admin_path = root / 'internal/gateway/admin.go'
app_path = root / 'internal/web/app.js'
workflow_path = root / '.github/workflows/apply-usage-retention-patch.yml'

stats = stats_path.read_text(encoding='utf-8')
original = stats
stats = stats.replace('trendRetention  = 7 * 24 * time.Hour', 'trendRetention  = 30 * 24 * time.Hour')
if stats == original:
    raise SystemExit('trendRetention pattern was not found')

# Add real token totals to every retained trend bucket and exposed point.
if 'InputTokens  int64  `json:"input_tokens"`' not in stats:
    stats, count = re.subn(
        r'(\n\s*Failed\s+int64\s+`json:"failed"`\s*\n)(\s*P95LatencyMS)',
        r'\1\tInputTokens  int64  `json:"input_tokens"`\n\tOutputTokens int64  `json:"output_tokens"`\n\tCachedTokens int64  `json:"cached_tokens"`\n\tTotalTokens  int64  `json:"total_tokens"`\n\2',
        stats,
        count=1,
    )
    if count != 1:
        raise SystemExit('TrendPoint insertion pattern was not found')

if 'InputTokens  int64\n\tOutputTokens int64' not in stats:
    stats, count = re.subn(
        r'(type trendBucket struct \{[\s\S]*?\n\s*Failed\s+int64\s*\n)(\s*Latencies)',
        r'\1\tInputTokens  int64\n\tOutputTokens int64\n\tCachedTokens int64\n\tTotalTokens  int64\n\2',
        stats,
        count=1,
    )
    if count != 1:
        raise SystemExit('trendBucket insertion pattern was not found')

if 'bucket.TotalTokens += record.TotalTokens' not in stats:
    marker = 'bucket.Latencies = append(bucket.Latencies, record.LatencyMS)'
    if marker not in stats:
        raise SystemExit('Stats.Record latency marker was not found')
    stats = stats.replace(marker, '''if record.UsageAvailable {
\t\tbucket.InputTokens += record.InputTokens
\t\tbucket.OutputTokens += record.OutputTokens
\t\tbucket.CachedTokens += record.CachedTokens
\t\tbucket.TotalTokens += record.TotalTokens
\t}
\t''' + marker, 1)

if 'InputTokens:  bucket.InputTokens' not in stats:
    stats, count = re.subn(
        r'(point\s*:=\s*TrendPoint\s*\{[\s\S]*?\n\s*Failed:\s*bucket\.Failed,\s*\n)',
        r'\1\t\t\tInputTokens:  bucket.InputTokens,\n\t\t\tOutputTokens: bucket.OutputTokens,\n\t\t\tCachedTokens: bucket.CachedTokens,\n\t\t\tTotalTokens:  bucket.TotalTokens,\n',
        stats,
        count=1,
    )
    if count != 1:
        raise SystemExit('TrendPoint population pattern was not found')

stats_path.write_text(stats, encoding='utf-8')

admin = admin_path.read_text(encoding='utf-8')
if 'case "30d":' not in admin:
    admin = admin.replace('''\tcase "7d":
\t\treturn trendRetention, nil
\tcase "all":''', '''\tcase "7d":
\t\treturn 7 * 24 * time.Hour, nil
\tcase "30d":
\t\treturn trendRetention, nil
\tcase "all":''')
admin = admin.replace('range must be one of 1h, 6h, 24h, 3d, 7d or all', 'range must be one of 1h, 6h, 24h, 3d, 7d, 30d or all')
admin_path.write_text(admin, encoding='utf-8')

app = app_path.read_text(encoding='utf-8')
old_request = "request(`/trends?range=${APP.range==='30d'?'7d':APP.range}`)"
if old_request not in app:
    raise SystemExit('canonical trend request pattern was not found')
app = app.replace(old_request, "request(`/trends?range=${APP.range}`)", 1)

old_head = "const records=recordsInRange(),successful=records.filter(requestOK).length,failed=records.length-successful,successRate=records.length?successful/records.length*100:null,latencies=records.map(record=>Number(record.latency_ms)).filter(Number.isFinite),p95=percentile(latencies,.95),quota=tightestQuota();"
new_head = "const records=recordsInRange(),trendPoints=!APP.accountFilter&&Array.isArray(APP.trend?.points)?APP.trend.points:[],trendBacked=trendPoints.length>0,callCount=trendBacked?trendPoints.reduce((sum,point)=>sum+(Number(point.requests)||0),0):records.length,failed=trendBacked?trendPoints.reduce((sum,point)=>sum+(Number(point.failed)||0),0):records.filter(record=>!requestOK(record)).length,successful=Math.max(0,callCount-failed),successRate=callCount?successful/callCount*100:null,latencies=records.map(record=>Number(record.latency_ms)).filter(Number.isFinite),p95=percentile(latencies,.95),quota=tightestQuota();"
if old_head not in app:
    raise SystemExit('renderUsage aggregation pattern was not found')
app = app.replace(old_head, new_head, 1)
app = app.replace("$('callsKpi').textContent=number(records.length);$('callsKpiNote').textContent=`${RANGE_LABEL[APP.range]} · ${failed} 次失败 · 最近样本上限 ${APP.data.stats?.recent_limit||records.length}`;", "$('callsKpi').textContent=number(callCount);$('callsKpiNote').textContent=trendBacked?`${RANGE_LABEL[APP.range]} · 服务端时间桶 · ${failed} 次失败`:`${RANGE_LABEL[APP.range]} · 指定渠道最近样本 · ${failed} 次失败`;", 1)
app = app.replace("$('successKpi').textContent=successRate===null?'—':`${successRate.toFixed(2)}%`;$('successKpiNote').textContent=records.length?`${successful} 成功 / ${failed} 失败`:'没有真实请求';", "$('successKpi').textContent=successRate===null?'—':`${successRate.toFixed(2)}%`;$('successKpiNote').textContent=callCount?`${successful} 成功 / ${failed} 失败`:'没有真实请求';", 1)
app = app.replace("if(records.length)clauses.push(`${RANGE_LABEL[APP.range]}调用 ${number(records.length)} 次，成功率 ${successRate.toFixed(2)}%`);", "if(callCount)clauses.push(`${RANGE_LABEL[APP.range]}调用 ${number(callCount)} 次，成功率 ${successRate.toFixed(2)}%`);", 1)

old_series = "if(APP.accountFilter||['tokens','success'].includes(APP.metric))return chartBucketsFromRecords(APP.metric);if(APP.trend?.points?.length&&['requests','latency'].includes(APP.metric)){return APP.trend.points.map(point=>({time:new Date(point.time).getTime(),value:APP.metric==='requests'?Number(point.requests):point.p95_latency_ms===null?null:Number(point.p95_latency_ms),count:Number(point.requests)||0})).filter(point=>Number.isFinite(point.time))}return chartBucketsFromRecords(APP.metric)"
new_series = "if(APP.accountFilter)return chartBucketsFromRecords(APP.metric);if(APP.trend?.points?.length&&['requests','tokens','success','latency'].includes(APP.metric)){return APP.trend.points.map(point=>{const requests=Number(point.requests)||0,failed=Number(point.failed)||0;let value=null;if(APP.metric==='requests')value=requests;if(APP.metric==='tokens')value=Number(point.total_tokens)||0;if(APP.metric==='success')value=requests?Math.max(0,requests-failed)/requests*100:null;if(APP.metric==='latency')value=point.p95_latency_ms===null?null:Number(point.p95_latency_ms);return{time:new Date(point.time).getTime(),value,count:requests}}).filter(point=>Number.isFinite(point.time))}return chartBucketsFromRecords(APP.metric)"
if old_series not in app:
    raise SystemExit('chartSeries pattern was not found')
app = app.replace(old_series, new_series, 1)
app_path.write_text(app, encoding='utf-8')

Path(__file__).unlink()
if workflow_path.exists():
    workflow_path.unlink()
