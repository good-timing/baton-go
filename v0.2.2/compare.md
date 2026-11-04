## go: 0.2.1 published vs 0.2.2 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.2.1 published | 0.2.2 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0748 (0.0737–0.0761) | 0.0763 (0.0741–0.0778) | +2% |
| overhead | plain_p99_ms | 0.387 (0.371–0.406) | 0.39 (0.376–0.399) | +1% (noise) |
| overhead | baton_p50_ms | 0.109 (0.108–0.113) | 0.111 (0.108–0.112) | +2% (noise) |
| overhead | baton_p99_ms | 0.609 (0.581–0.654) | 0.62 (0.575–0.641) | +2% (noise) |
| overhead | added_p50_ms | 0.0352 (0.0331–0.0389) | 0.0342 (0.0317–0.0376) | -3% (noise) |
| overhead | added_p99_ms | 0.209 (0.178–0.27) | 0.238 (0.185–0.252) | +14% (noise) |
| burst | wall_ms | 1,551 (1,534–1,553) | 1,569 (1,562–1,584) | +1% |
| burst | call_p50_ms | 1.49 (1.46–1.5) | 1.5 (1.49–1.52) | +1% (noise) |
| burst | call_p99_ms | 3.43 (3.34–3.47) | 3.43 (3.4–3.71) | +0% (noise) |
| burst | close_ms | 2.1 (1.85–2.41) | 2.08 (1.84–2.21) | -1% (noise) |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 285 (280–289) | 283 (280–290) | -1% (noise) |
| outage_hang | call_p50_ms | 1.38 (1.37–1.39) | 1.38 (1.36–1.42) | +0% (noise) |
| outage_hang | call_p99_ms | 3.08 (2.89–3.28) | 2.92 (2.86–3) | -5% (noise) |
| outage_hang | close_ms | 2,901 (2,900–2,901) | 2,901 (2,900–2,901) | -0% (noise) |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 289 (283–292) | 286 (283–291) | -1% (noise) |
| outage_down | call_p50_ms | 1.39 (1.37–1.4) | 1.39 (1.36–1.41) | +0% (noise) |
| outage_down | call_p99_ms | 3.11 (2.97–3.23) | 3.04 (2.9–3.15) | -2% (noise) |
| outage_down | close_ms | 1,126 (1,121–1,133) | 1,129 (1,123–1,131) | +0% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 973,256 (926,984–1,015,448) | 972,200 (936,712–1,009,880) | -0% (noise) |
| memory | heap_at_half_bytes | 1,011,304 (972,304–1,022,960) | 1,012,328 (972,584–1,017,992) | +0% (noise) |
| memory | heap_at_end_bytes | 978,728 (937,544–1,098,752) | 1,007,976 (980,432–1,014,360) | +3% (noise) |
| memory | growth_per_call_bytes | -0.256 (-4.42–8.74) | 0.203 (-0.565–3.48) | +0.459 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,291,976 (1,283,528–1,297,904) | 1,290,280 (1,284,280–1,293,528) | -0% (noise) |
| memory_outage | heap_at_half_bytes | 2,143,224 (2,138,888–2,148,792) | 2,141,736 (2,137,416–2,148,584) | -0% (noise) |
| memory_outage | heap_at_end_bytes | 2,134,720 (2,128,256–2,136,528) | 2,135,216 (2,126,848–2,143,472) | +0% (noise) |
| memory_outage | growth_per_call_bytes | -1.06 (-1.41–-0.572) | -1.03 (-1.49–-0.292) | +3% (noise) |
| large_result | plain_p50_ms | 1.1 (1.09–1.12) | 1.04 (1.03–1.05) | -5% |
| large_result | plain_p99_ms | 1.43 (1.41–1.83) | 1.34 (1.33–1.39) | -6% |
| large_result | baton_p50_ms | 11 (11–11.1) | 2.08 (2.06–2.12) | -81% |
| large_result | baton_p99_ms | 13.4 (12.8–13.6) | 3.49 (3.28–3.7) | -74% |
| large_result | added_p50_ms | 9.89 (9.88–9.99) | 1.04 (1.02–1.07) | -89% |
| large_result | added_p99_ms | 11.8 (11.4–12.1) | 2.12 (1.95–2.35) | -82% |
| large_result | heap_growth_bytes | 1,409,216 (1,223,416–1,478,976) | 1,530,664 (1,354,312–1,635,720) | +9% |
| large_result | emitted | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | delivered | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | lost | 0 (0–0) | 0 (0–0) | same |
| process | max_rss_bytes | 20,795,392 (20,213,760–23,695,360) | 20,836,352 (20,709,376–21,102,592) | +0% (noise) |
