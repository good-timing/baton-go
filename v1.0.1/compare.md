## go: 1.0.0 published vs 1.0.1 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 1.0.0 published | 1.0.1 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0526 (0.0519–0.0541) | 0.0533 (0.0526–0.0535) | +1% (noise) |
| overhead | plain_p99_ms | 0.315 (0.303–0.333) | 0.308 (0.287–0.328) | -2% (noise) |
| overhead | baton_p50_ms | 0.0846 (0.082–0.0853) | 0.0841 (0.0835–0.0855) | -1% (noise) |
| overhead | baton_p99_ms | 0.485 (0.479–0.542) | 0.49 (0.452–0.502) | +1% (noise) |
| overhead | added_p50_ms | 0.0313 (0.0294–0.0322) | 0.031 (0.0307–0.0323) | -1% (noise) |
| overhead | added_p99_ms | 0.177 (0.158–0.209) | 0.171 (0.144–0.215) | -3% (noise) |
| burst | wall_ms | 1,264 (1,245–1,288) | 1,255 (1,224–1,270) | -1% (noise) |
| burst | call_p50_ms | 1.22 (1.19–1.22) | 1.19 (1.16–1.21) | -2% (noise) |
| burst | call_p99_ms | 2.9 (2.78–3.1) | 2.88 (2.79–3.03) | -1% (noise) |
| burst | close_ms | 2.23 (1.77–2.38) | 1.96 (1.63–2.04) | -12% (noise) |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 202 (201–208) | 203 (199–204) | +0% (noise) |
| outage_hang | call_p50_ms | 1 (0.989–1.02) | 0.978 (0.967–1) | -2% |
| outage_hang | call_p99_ms | 2.17 (2.11–2.24) | 2.28 (2.18–2.41) | +5% |
| outage_hang | close_ms | 2,900 (2,900–2,901) | 2,900 (2,900–2,901) | -0% |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 205 (203–209) | 204 (203–207) | -1% (noise) |
| outage_down | call_p50_ms | 1 (0.987–1.01) | 0.993 (0.983–0.998) | -1% (noise) |
| outage_down | call_p99_ms | 2.27 (2.24–2.51) | 2.24 (2.21–2.28) | -1% (noise) |
| outage_down | close_ms | 1,207 (1,203–1,209) | 1,208 (1,204–1,211) | +0% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 1,017,176 (975,776–1,094,248) | 1,044,528 (1,012,848–1,054,336) | +3% (noise) |
| memory | heap_at_half_bytes | 1,014,392 (981,040–1,057,744) | 1,023,976 (973,968–1,053,392) | +1% (noise) |
| memory | heap_at_end_bytes | 1,053,992 (1,015,984–1,103,216) | 1,053,168 (988,856–1,059,456) | -0% (noise) |
| memory | growth_per_call_bytes | 0.424 (-2.56–11.6) | 0.723 (-6.45–8.55) | +0.299 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,330,768 (1,329,328–1,336,656) | 1,325,776 (1,323,248–1,331,744) | -0% |
| memory_outage | heap_at_half_bytes | 2,267,936 (2,266,592–2,274,336) | 2,255,584 (2,249,680–2,256,544) | -1% |
| memory_outage | heap_at_end_bytes | 2,270,464 (2,264,016–2,274,544) | 2,252,032 (2,243,760–2,264,064) | -1% |
| memory_outage | growth_per_call_bytes | 0.269 (-0.97–0.795) | -0.29 (-1.18–1.18) | -0.558 (noise) |
| large_result | plain_p50_ms | 0.934 (0.929–0.94) | 0.94 (0.936–0.972) | +1% (noise) |
| large_result | plain_p99_ms | 1.19 (1.17–1.22) | 1.21 (1.18–1.93) | +2% (noise) |
| large_result | baton_p50_ms | 1.94 (1.9–1.96) | 1.4 (1.39–1.42) | -28% |
| large_result | baton_p99_ms | 3.36 (3.12–3.48) | 2.86 (2.61–2.99) | -15% |
| large_result | added_p50_ms | 1 (0.969–1.02) | 0.46 (0.421–0.485) | -54% |
| large_result | added_p99_ms | 2.16 (1.92–2.29) | 1.68 (0.677–1.78) | -22% |
| large_result | heap_growth_bytes | 1,355,032 (1,169,136–1,588,032) | 629,832 (606,224–895,848) | -54% |
| large_result | emitted | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | delivered | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | lost | 0 (0–0) | 0 (0–0) | same |
| process | max_rss_bytes | 21,082,112 (20,869,120–21,413,888) | 20,955,136 (20,865,024–21,241,856) | -1% (noise) |
