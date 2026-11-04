## go: 0.2.0 published vs 0.2.1 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.2.0 published | 0.2.1 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0881 (0.086–0.0905) | 0.0894 (0.0874–0.0916) | +1% (noise) |
| overhead | plain_p99_ms | 0.459 (0.437–0.475) | 0.497 (0.459–0.561) | +8% |
| overhead | baton_p50_ms | 0.141 (0.136–0.142) | 0.14 (0.14–0.15) | -1% (noise) |
| overhead | baton_p99_ms | 0.689 (0.678–0.732) | 0.695 (0.685–1.27) | +1% (noise) |
| overhead | added_p50_ms | 0.052 (0.0501–0.0542) | 0.0503 (0.0484–0.0622) | -3% (noise) |
| overhead | added_p99_ms | 0.252 (0.202–0.273) | 0.198 (0.124–0.755) | -22% |
| burst | wall_ms | 1,779 (1,745–1,799) | 1,793 (1,741–1,869) | +1% (noise) |
| burst | call_p50_ms | 1.7 (1.68–1.71) | 1.72 (1.67–1.77) | +1% |
| burst | call_p99_ms | 4.02 (3.79–4.04) | 3.94 (3.76–4.14) | -2% (noise) |
| burst | close_ms | 2.43 (2.08–2.88) | 2.21 (1.84–2.6) | -9% (noise) |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 306 (305–311) | 309 (305–315) | +1% (noise) |
| outage_hang | call_p50_ms | 1.46 (1.45–1.5) | 1.48 (1.48–1.51) | +1% (noise) |
| outage_hang | call_p99_ms | 3.36 (3.24–3.51) | 3.34 (3.28–3.45) | -1% (noise) |
| outage_hang | close_ms | 2,901 (2,900–2,901) | 2,901 (2,900–2,901) | -0% (noise) |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 305 (301–312) | 309 (299–315) | +1% (noise) |
| outage_down | call_p50_ms | 1.45 (1.45–1.51) | 1.49 (1.43–1.51) | +2% (noise) |
| outage_down | call_p99_ms | 3.25 (3.2–3.44) | 3.34 (3.21–3.46) | +3% (noise) |
| outage_down | close_ms | 1,110 (1,103–1,114) | 1,106 (1,101–1,115) | -0% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 995,440 (971,232–1,019,136) | 973,472 (926,016–977,960) | -2% (noise) |
| memory | heap_at_half_bytes | 1,002,440 (973,880–1,015,768) | 972,192 (939,312–1,004,712) | -3% |
| memory | heap_at_end_bytes | 973,536 (946,296–1,019,736) | 966,408 (935,128–975,160) | -1% (noise) |
| memory | growth_per_call_bytes | -3.39 (-5.36–0.571) | -2.21 (-4.43–2.71) | +1.18 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,287,304 (1,280,872–1,291,592) | 1,286,632 (1,282,488–1,295,944) | -0% (noise) |
| memory_outage | heap_at_half_bytes | 2,135,640 (2,130,480–2,146,680) | 2,145,304 (2,125,520–2,147,944) | +0% (noise) |
| memory_outage | heap_at_end_bytes | 2,141,912 (2,140,840–2,150,744) | 2,144,424 (2,136,888–2,146,552) | +0% (noise) |
| memory_outage | growth_per_call_bytes | 0.533 (0.357–2.03) | -0.0064 (-0.158–1.14) | -101% |
| process | max_rss_bytes | 21,078,016 (20,856,832–21,712,896) | 21,032,960 (20,729,856–22,560,768) | -0% (noise) |
