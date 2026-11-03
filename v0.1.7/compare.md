## go: 0.1.6 published vs 0.1.7 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.1.6 published | 0.1.7 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0556 (0.0531–0.0564) | 0.055 (0.0549–0.0564) | -1% (noise) |
| overhead | plain_p99_ms | 0.316 (0.301–0.339) | 0.328 (0.307–0.332) | +4% (noise) |
| overhead | baton_p50_ms | 0.0836 (0.0825–0.0856) | 0.0846 (0.0837–0.0863) | +1% (noise) |
| overhead | baton_p99_ms | 0.47 (0.461–0.484) | 0.492 (0.486–0.527) | +5% |
| overhead | added_p50_ms | 0.028 (0.0264–0.0325) | 0.0296 (0.0288–0.03) | +6% (noise) |
| overhead | added_p99_ms | 0.145 (0.127–0.177) | 0.18 (0.16–0.212) | +24% |
| burst | wall_ms | 1,208 (1,193–1,211) | 1,225 (1,221–1,253) | +1% |
| burst | call_p50_ms | 1.15 (1.14–1.16) | 1.16 (1.15–1.2) | +1% |
| burst | call_p99_ms | 2.72 (2.57–2.75) | 2.79 (2.74–2.83) | +3% |
| burst | close_ms | 1.73 (1.4–1.82) | 1.64 (0.586–1.97) | -5% (noise) |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 211 (205–213) | 210 (208–212) | -1% (noise) |
| outage_hang | call_p50_ms | 1.01 (1–1.04) | 1.01 (1–1.02) | -0% (noise) |
| outage_hang | call_p99_ms | 2.23 (2.21–2.33) | 2.26 (2.19–2.36) | +2% (noise) |
| outage_hang | close_ms | 2,901 (2,900–2,901) | 2,901 (2,900–2,901) | +0% (noise) |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 214 (211–216) | 210 (209–215) | -2% |
| outage_down | call_p50_ms | 1.03 (1.02–1.03) | 1.01 (1–1.02) | -1% |
| outage_down | call_p99_ms | 2.31 (2.24–2.55) | 2.31 (2.21–2.68) | -0% (noise) |
| outage_down | close_ms | 1,198 (1,196–1,201) | 1,201 (1,196–1,202) | +0% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 1,018,392 (969,016–1,057,336) | 1,004,320 (964,776–1,019,320) | -1% (noise) |
| memory | heap_at_half_bytes | 979,880 (926,800–1,007,168) | 1,056,544 (970,864–1,058,664) | +8% |
| memory | heap_at_end_bytes | 1,008,416 (974,264–1,015,040) | 1,013,592 (972,128–1,015,440) | +1% (noise) |
| memory | growth_per_call_bytes | 2.7 (0.125–4.87) | -4.32 (-4.61–0.126) | -260% |
| memory_outage | heap_after_warmup_bytes | 1,285,664 (1,284,512–1,288,736) | 1,289,632 (1,286,064–1,294,112) | +0% |
| memory_outage | heap_at_half_bytes | 2,144,352 (2,135,712–2,148,288) | 2,147,456 (2,133,648–2,147,992) | +0% (noise) |
| memory_outage | heap_at_end_bytes | 2,145,216 (2,139,376–2,150,176) | 2,143,520 (2,137,680–2,156,416) | -0% (noise) |
| memory_outage | growth_per_call_bytes | 0.149 (-0.272–0.619) | 0.114 (-0.482–0.842) | -0.0352 (noise) |
| process | max_rss_bytes | 21,151,744 (20,291,584–22,872,064) | 20,905,984 (20,717,568–21,516,288) | -1% (noise) |
