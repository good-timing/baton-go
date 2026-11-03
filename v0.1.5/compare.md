## go: 0.1.4 published vs 0.1.5 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. Median (lowest–highest) of 5, 5 runs.

| measure | metric | 0.1.4 published | 0.1.5 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0909 (0.0894–0.091) | 0.0904 (0.0898–0.0907) | -1% (noise) |
| overhead | plain_p99_ms | 0.486 (0.464–0.515) | 0.477 (0.444–0.504) | -2% (noise) |
| overhead | baton_p50_ms | 0.187 (0.183–0.19) | 0.141 (0.136–0.141) | -25% |
| overhead | baton_p99_ms | 0.911 (0.796–0.957) | 0.674 (0.664–0.682) | -26% |
| overhead | added_p50_ms | 0.0961 (0.0919–0.101) | 0.0504 (0.0465–0.0514) | -48% |
| overhead | added_p99_ms | 0.422 (0.3–0.493) | 0.197 (0.163–0.236) | -53% |
| burst | wall_ms | 2,042 (2,033–2,086) | 1,761 (1,742–1,772) | -14% |
| burst | call_p50_ms | 1.92 (1.91–1.96) | 1.68 (1.66–1.69) | -12% |
| burst | call_p99_ms | 4.53 (4.38–4.55) | 3.85 (3.8–3.93) | -15% |
| burst | close_ms | 498 (497–499) | 2.09 (1.9–2.49) | -100% |
| burst | emitted | 20,000 (20,000–20,000) | 20,000 (20,000–20,000) | same |
| burst | delivered | 2,600 (2,558–2,642) | 20,000 (20,000–20,000) | +669% |
| burst | lost | 17,400 (17,358–17,442) | 0 (0–0) | -100% |
| outage_hang | wall_ms | 324 (322–326) | 309 (300–312) | -5% |
| outage_hang | call_p50_ms | 1.51 (1.5–1.53) | 1.49 (1.44–1.52) | -2% |
| outage_hang | call_p99_ms | 3.34 (3.22–3.53) | 3.27 (3.18–3.34) | -2% (noise) |
| outage_hang | close_ms | 3,001 (3,000–3,001) | 3,000 (3,000–3,001) | -0% (noise) |
| outage_hang | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | wall_ms | 326 (323–425) | 310 (308–314) | -5% |
| outage_down | call_p50_ms | 1.53 (1.51–1.89) | 1.48 (1.45–1.54) | -3% |
| outage_down | call_p99_ms | 3.46 (3.3–6.26) | 3.37 (3.25–3.66) | -3% (noise) |
| outage_down | close_ms | 1,083 (1,072–1,785) | 1,104 (1,100–1,105) | +2% (noise) |
| outage_down | emitted | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 4,000 (4,000–4,000) | 4,000 (4,000–4,000) | same |
| memory | heap_after_warmup_bytes | 1,173,408 (1,144,144–1,197,632) | 1,003,680 (668,984–1,044,272) | -14% |
| memory | heap_at_half_bytes | 1,983,760 (1,927,120–2,028,240) | 968,608 (931,592–1,005,584) | -51% |
| memory | heap_at_end_bytes | 1,991,776 (1,962,400–2,014,992) | 1,005,832 (971,168–1,014,928) | -50% |
| memory | growth_per_call_bytes | 1.52 (-5.54–8.79) | 1.45 (-2.97–8.33) | -0.0704 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,097,336 (1,089,424–1,104,232) | 1,285,952 (1,284,464–1,298,544) | +17% |
| memory_outage | heap_at_half_bytes | 1,850,736 (1,844,816–1,862,768) | 2,145,168 (2,135,424–2,146,880) | +16% |
| memory_outage | heap_at_end_bytes | 1,863,272 (1,859,448–1,867,848) | 2,146,112 (2,139,568–2,150,400) | +15% |
| memory_outage | growth_per_call_bytes | 1.71 (-0.332–2.05) | 0.0944 (-0.0288–0.571) | -1.62 (noise) |
| process | max_rss_bytes | 22,286,336 (21,430,272–25,370,624) | 20,922,368 (20,721,664–21,458,944) | -6% |
