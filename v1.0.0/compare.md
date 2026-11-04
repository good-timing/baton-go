## go: 0.3.0 published vs 1.0.0 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.3.0 published | 1.0.0 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0894 (0.0889–0.0912) | 0.0906 (0.0898–0.0912) | +1% (noise) |
| overhead | plain_p99_ms | 0.481 (0.463–0.513) | 0.496 (0.475–0.522) | +3% (noise) |
| overhead | baton_p50_ms | 0.141 (0.139–0.143) | 0.144 (0.143–0.146) | +3% |
| overhead | baton_p99_ms | 0.698 (0.687–0.727) | 0.705 (0.693–0.759) | +1% (noise) |
| overhead | added_p50_ms | 0.0503 (0.0491–0.0546) | 0.0537 (0.0525–0.0552) | +7% (noise) |
| overhead | added_p99_ms | 0.208 (0.175–0.258) | 0.229 (0.197–0.249) | +10% (noise) |
| burst | wall_ms | 1,781 (1,764–2,194) | 1,805 (1,795–1,848) | +1% (noise) |
| burst | call_p50_ms | 1.7 (1.68–2.04) | 1.73 (1.72–1.77) | +2% (noise) |
| burst | call_p99_ms | 3.99 (3.9–5.42) | 4.02 (3.91–4.14) | +1% (noise) |
| burst | close_ms | 2.45 (2.09–2.81) | 2.41 (0.912–2.67) | -1% (noise) |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 307 (302–321) | 304 (296–311) | -1% (noise) |
| outage_hang | call_p50_ms | 1.48 (1.47–1.53) | 1.49 (1.43–1.51) | +0% (noise) |
| outage_hang | call_p99_ms | 3.32 (3.17–3.48) | 3.32 (3.18–3.51) | +0% (noise) |
| outage_hang | close_ms | 2,901 (2,900–2,901) | 2,901 (2,900–2,901) | +0% (noise) |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 311 (310–318) | 303 (301–312) | -2% |
| outage_down | call_p50_ms | 1.49 (1.47–1.52) | 1.47 (1.45–1.5) | -1% (noise) |
| outage_down | call_p99_ms | 3.27 (3.21–3.48) | 3.41 (3.34–3.54) | +4% (noise) |
| outage_down | close_ms | 1,104 (1,099–1,106) | 1,112 (1,103–1,113) | +1% |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 999,232 (928,096–1,011,600) | 1,028,848 (1,011,248–1,058,912) | +3% |
| memory | heap_at_half_bytes | 1,004,000 (932,496–1,024,872) | 1,051,376 (1,037,976–1,060,400) | +5% |
| memory | heap_at_end_bytes | 975,808 (945,160–1,011,968) | 1,052,192 (1,018,336–1,064,600) | +8% |
| memory | growth_per_call_bytes | 0.218 (-7.97–7.95) | -0.403 (-3.3–2.66) | -0.622 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,289,552 (1,280,928–1,293,504) | 1,330,960 (1,327,312–1,338,416) | +3% |
| memory_outage | heap_at_half_bytes | 2,138,160 (2,136,464–2,142,256) | 2,274,560 (2,263,520–2,275,680) | +6% |
| memory_outage | heap_at_end_bytes | 2,143,072 (2,134,800–2,151,824) | 2,268,816 (2,263,456–2,275,728) | +6% |
| memory_outage | growth_per_call_bytes | 0.491 (-0.166–1.47) | 0.0048 (-1.11–0.31) | -0.486 (noise) |
| large_result | plain_p50_ms | 1.36 (1.34–1.37) | 1.36 (1.34–1.4) | -0% (noise) |
| large_result | plain_p99_ms | 1.75 (1.72–1.87) | 1.77 (1.69–1.84) | +1% (noise) |
| large_result | baton_p50_ms | 2.55 (2.53–2.59) | 2.62 (2.57–2.66) | +3% |
| large_result | baton_p99_ms | 4.62 (4.53–4.74) | 4.58 (4.34–4.86) | -1% (noise) |
| large_result | added_p50_ms | 1.21 (1.18–1.22) | 1.26 (1.23–1.27) | +4% |
| large_result | added_p99_ms | 2.86 (2.8–2.99) | 2.83 (2.65–3.08) | -1% (noise) |
| large_result | heap_growth_bytes | 1,411,056 (1,110,096–1,419,424) | 1,418,240 (1,309,824–1,521,408) | +1% (noise) |
| large_result | emitted | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | delivered | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | lost | 0 (0–0) | 0 (0–0) | same |
| process | max_rss_bytes | 21,704,704 (20,901,888–22,806,528) | 21,450,752 (21,143,552–21,671,936) | -1% (noise) |
