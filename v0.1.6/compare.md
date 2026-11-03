## go: 0.1.5 published vs 0.1.6 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.1.5 published | 0.1.6 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0896 (0.0878–0.0907) | 0.0894 (0.0876–0.0901) | -0% (noise) |
| overhead | plain_p99_ms | 0.474 (0.42–0.491) | 0.478 (0.452–0.506) | +1% (noise) |
| overhead | baton_p50_ms | 0.137 (0.136–0.14) | 0.139 (0.136–0.142) | +1% (noise) |
| overhead | baton_p99_ms | 0.703 (0.633–0.738) | 0.664 (0.658–0.719) | -6% (noise) |
| overhead | added_p50_ms | 0.0479 (0.0452–0.0504) | 0.0516 (0.0469–0.0517) | +8% |
| overhead | added_p99_ms | 0.238 (0.158–0.256) | 0.204 (0.157–0.245) | -14% (noise) |
| burst | wall_ms | 1,742 (1,736–1,758) | 1,745 (1,733–1,988) | +0% (noise) |
| burst | call_p50_ms | 1.67 (1.65–1.7) | 1.67 (1.66–1.85) | +0% (noise) |
| burst | call_p99_ms | 3.8 (3.78–3.83) | 3.86 (3.64–4.84) | +2% |
| burst | close_ms | 2.16 (1.96–2.49) | 2.22 (2.03–2.25) | +3% (noise) |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | wall_ms | 304 (302–308) | 309 (305–388) | +2% |
| outage_hang | call_p50_ms | 1.47 (1.44–1.49) | 1.48 (1.46–1.83) | +1% (noise) |
| outage_hang | call_p99_ms | 3.33 (3.29–3.38) | 3.45 (3.19–4.8) | +4% |
| outage_hang | close_ms | 3,001 (3,000–3,001) | 2,901 (2,900–2,901) | -3% |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 311 (306–315) | 307 (305–310) | -1% (noise) |
| outage_down | call_p50_ms | 1.49 (1.47–1.52) | 1.48 (1.47–1.49) | -1% (noise) |
| outage_down | call_p99_ms | 3.38 (3.26–3.47) | 3.32 (3.22–3.44) | -2% (noise) |
| outage_down | close_ms | 1,102 (1,100–1,109) | 1,108 (1,104–1,112) | +1% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 996,152 (966,600–1,008,112) | 998,768 (966,864–1,055,528) | +0% (noise) |
| memory | heap_at_half_bytes | 1,007,960 (1,002,048–1,013,520) | 971,104 (969,744–1,187,280) | -4% |
| memory | heap_at_end_bytes | 1,010,192 (1,002,464–1,014,144) | 976,536 (969,992–1,009,408) | -3% |
| memory | growth_per_call_bytes | 0.0624 (-0.11–0.41) | 0.0248 (-21.3–3.83) | -0.0376 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,286,528 (1,282,320–1,294,768) | 1,289,104 (1,284,656–1,292,464) | +0% (noise) |
| memory_outage | heap_at_half_bytes | 2,145,968 (2,135,488–2,146,784) | 2,140,448 (2,138,768–2,144,064) | -0% (noise) |
| memory_outage | heap_at_end_bytes | 2,147,648 (2,143,760–2,150,064) | 2,143,792 (2,141,872–2,146,944) | -0% (noise) |
| memory_outage | growth_per_call_bytes | 0.328 (-0.0816–1.25) | 0.31 (-0.0272–0.674) | -0.0176 (noise) |
| process | max_rss_bytes | 20,832,256 (20,254,720–21,037,056) | 21,098,496 (20,566,016–21,422,080) | +1% |
