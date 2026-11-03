## go: 0.1.7 published vs 0.2.0 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.1.7 published | 0.2.0 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0892 (0.0884–0.0904) | 0.0896 (0.0894–0.0909) | +0% (noise) |
| overhead | plain_p99_ms | 0.478 (0.445–0.491) | 0.465 (0.46–0.503) | -3% (noise) |
| overhead | baton_p50_ms | 0.14 (0.136–0.141) | 0.139 (0.139–0.141) | -0% (noise) |
| overhead | baton_p99_ms | 0.688 (0.665–0.74) | 0.669 (0.649–0.698) | -3% (noise) |
| overhead | added_p50_ms | 0.0499 (0.0479–0.0521) | 0.0496 (0.0492–0.0509) | -1% (noise) |
| overhead | added_p99_ms | 0.22 (0.198–0.249) | 0.209 (0.16–0.233) | -5% (noise) |
| burst | wall_ms | 1,776 (1,755–1,869) | 1,782 (1,763–1,884) | +0% (noise) |
| burst | call_p50_ms | 1.71 (1.68–1.77) | 1.71 (1.69–1.82) | +0% (noise) |
| burst | call_p99_ms | 3.9 (3.86–4.49) | 3.96 (3.85–4.32) | +2% (noise) |
| burst | close_ms | 2.23 (2.08–2.34) | 2.49 (2.09–2.56) | +12% |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 305 (303–310) | 314 (307–380) | +3% |
| outage_hang | call_p50_ms | 1.48 (1.46–1.48) | 1.5 (1.49–1.78) | +1% |
| outage_hang | call_p99_ms | 3.25 (3.21–3.47) | 3.37 (3.34–4.88) | +4% (noise) |
| outage_hang | close_ms | 2,900 (2,900–2,901) | 2,900 (2,900–2,901) | -0% (noise) |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 310 (308–318) | 308 (304–311) | -1% (noise) |
| outage_down | call_p50_ms | 1.49 (1.47–1.52) | 1.48 (1.45–1.49) | -0% (noise) |
| outage_down | call_p99_ms | 3.47 (3.32–3.56) | 3.28 (3.22–3.41) | -5% |
| outage_down | close_ms | 1,103 (1,098–1,107) | 1,104 (1,103–1,110) | +0% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 967,144 (963,384–1,004,352) | 967,456 (930,280–998,408) | +0% (noise) |
| memory | heap_at_half_bytes | 1,005,216 (983,872–1,100,160) | 972,936 (968,136–1,010,848) | -3% |
| memory | heap_at_end_bytes | 1,012,016 (1,006,960–1,014,848) | 1,008,088 (944,400–1,016,136) | -0% (noise) |
| memory | growth_per_call_bytes | 0.264 (-8.56–2.81) | -0.0744 (-2.44–4.32) | -0.338 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,288,976 (1,284,864–1,295,648) | 1,288,200 (1,279,000–1,295,288) | -0% (noise) |
| memory_outage | heap_at_half_bytes | 2,144,048 (2,142,384–2,151,024) | 2,144,536 (2,138,552–2,150,584) | +0% (noise) |
| memory_outage | heap_at_end_bytes | 2,146,272 (2,139,248–2,150,928) | 2,145,960 (2,139,576–2,148,680) | -0% (noise) |
| memory_outage | growth_per_call_bytes | -0.0096 (-0.4–0.346) | -0.19 (-0.554–0.741) | -0.181 (noise) |
| process | max_rss_bytes | 20,639,744 (20,402,176–21,114,880) | 21,082,112 (20,934,656–21,929,984) | +2% (noise) |
