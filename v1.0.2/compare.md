## go: 1.0.1 published vs 1.0.2 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. The exception is a measure's 'limits': over 'repeats' runs of that measure alone, each named metric's median must be under its limit. scripts/check_bench_limit.py runs and checks them. A measure's 'noise_floor' gives, per metric and SDK, the change between runs of one version that is still noise. Median (lowest–highest) of 5, 5 runs.

CPU: 1.0.1 published: AMD EPYC 9V45 96-Core Processor (5); 1.0.2 checkout: AMD EPYC 9V45 96-Core Processor (5)

| measure | metric | 1.0.1 published | 1.0.2 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0345 (0.0325–0.035) | 0.0332 (0.0324–0.0344) | -4% (noise) |
| overhead | plain_p99_ms | 0.225 (0.216–0.235) | 0.224 (0.211–0.231) | -0% (noise) |
| overhead | baton_p50_ms | 0.0567 (0.0556–0.0591) | 0.0582 (0.0555–0.0609) | +3% (noise) |
| overhead | baton_p99_ms | 0.383 (0.353–0.395) | 0.398 (0.374–0.491) | +4% |
| overhead | added_p50_ms | 0.0228 (0.0217–0.0247) | 0.0253 (0.0211–0.0277) | +11% |
| overhead | added_p99_ms | 0.157 (0.128–0.177) | 0.174 (0.151–0.27) | +11% (noise) |
| burst | plain_wall_ms | 595 (577–603) | 591 (586–600) | -1% (noise) |
| burst | plain_call_p50_ms | 0.549 (0.531–0.558) | 0.547 (0.533–0.558) | -0% (noise) |
| burst | plain_call_p99_ms | 1.26 (1.21–1.31) | 1.26 (1.21–1.32) | +0% (noise) |
| burst | wall_ms | 931 (888–965) | 911 (884–958) | -2% (noise) |
| burst | call_p50_ms | 0.891 (0.85–0.914) | 0.87 (0.849–0.907) | -2% (noise) |
| burst | call_p99_ms | 2.12 (2.03–2.33) | 2.05 (1.98–2.31) | -3% (noise) |
| burst | added_call_p50_ms | 0.342 (0.32–0.368) | 0.337 (0.308–0.355) | -1% (noise) |
| burst | added_call_p99_ms | 0.857 (0.82–1.02) | 0.807 (0.716–1.09) | -6% |
| burst | close_ms | 1.5 (1.13–1.85) | 1.52 (1.31–1.6) | +1% (noise) |
| burst | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| burst | delivered | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | plain_wall_ms | 599 (576–607) | 593 (572–609) | -1% (noise) |
| outage_hang | plain_call_p50_ms | 0.557 (0.534–0.559) | 0.541 (0.53–0.562) | -3% (noise) |
| outage_hang | plain_call_p99_ms | 1.23 (1.18–1.3) | 1.26 (1.17–1.31) | +3% (noise) |
| outage_hang | wall_ms | 703 (663–710) | 696 (659–748) | -1% (noise) |
| outage_hang | call_p50_ms | 0.717 (0.676–0.729) | 0.712 (0.676–0.737) | -1% (noise) |
| outage_hang | call_p99_ms | 1.57 (1.5–1.62) | 1.57 (1.48–1.87) | -0% (noise) |
| outage_hang | added_call_p50_ms | 0.159 (0.141–0.172) | 0.17 (0.146–0.18) | +7% (noise) |
| outage_hang | added_call_p99_ms | 0.318 (0.302–0.346) | 0.349 (0.297–0.6) | +10% |
| outage_hang | close_ms | 2,900 (2,900–2,901) | 2,900 (2,900–2,901) | -0% (noise) |
| outage_hang | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_down | plain_wall_ms | 589 (576–607) | 593 (570–609) | +1% (noise) |
| outage_down | plain_call_p50_ms | 0.543 (0.532–0.556) | 0.541 (0.53–0.56) | -0% (noise) |
| outage_down | plain_call_p99_ms | 1.25 (1.21–1.26) | 1.28 (1.17–1.31) | +3% |
| outage_down | wall_ms | 678 (668–715) | 711 (675–717) | +5% (noise) |
| outage_down | call_p50_ms | 0.692 (0.674–0.726) | 0.702 (0.688–0.721) | +1% (noise) |
| outage_down | call_p99_ms | 1.54 (1.52–1.62) | 1.63 (1.49–1.86) | +6% |
| outage_down | added_call_p50_ms | 0.152 (0.142–0.17) | 0.162 (0.156–0.167) | +6% (noise) |
| outage_down | added_call_p99_ms | 0.33 (0.275–0.37) | 0.35 (0.32–0.548) | +6% (noise) |
| outage_down | close_ms | 713 (703–724) | 703 (702–718) | -2% |
| outage_down | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| memory | heap_after_warmup_bytes | 1,046,232 (1,044,600–1,052,656) | 1,021,048 (971,272–1,022,712) | -2% |
| memory | heap_at_half_bytes | 1,042,856 (984,952–1,140,888) | 1,018,152 (968,336–1,108,904) | -2% (noise) |
| memory | heap_at_end_bytes | 1,017,416 (997,800–1,090,672) | 1,045,672 (982,600–1,059,152) | +3% (noise) |
| memory | growth_per_call_bytes | -2.54 (-9–10.6) | -0.128 (-8.62–8.07) | +2.42 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,332,864 (1,314,712–1,341,960) | 1,324,296 (1,317,864–1,332,568) | -1% (noise) |
| memory_outage | heap_at_half_bytes | 2,256,832 (2,248,320–2,259,912) | 2,252,240 (2,244,048–2,261,896) | -0% (noise) |
| memory_outage | heap_at_end_bytes | 2,253,032 (2,239,256–2,260,936) | 2,252,296 (2,247,064–2,263,344) | -0% (noise) |
| memory_outage | growth_per_call_bytes | -0.308 (-0.906–0.194) | 0.0056 (-0.396–0.522) | +0.314 (noise) |
| large_result | plain_p50_ms | 0.652 (0.641–0.694) | 0.657 (0.64–0.678) | +1% (noise) |
| large_result | plain_p99_ms | 0.864 (0.83–1.08) | 0.872 (0.834–1.95) | +1% (noise) |
| large_result | baton_p50_ms | 0.96 (0.924–0.986) | 0.972 (0.936–1) | +1% (noise) |
| large_result | baton_p99_ms | 1.72 (1.51–2.62) | 1.75 (1.7–1.95) | +2% (noise) |
| large_result | added_p50_ms | 0.296 (0.267–0.334) | 0.296 (0.294–0.348) | -0% (noise) |
| large_result | added_p99_ms | 0.841 (0.642–1.64) | 0.888 (-0.248–1.07) | +6% (noise) |
| large_result | heap_growth_bytes | 625,424 (451,544–679,968) | 488,080 (456,208–784,224) | -22% (noise) |
| large_result | emitted | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | delivered | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | lost | 0 (0–0) | 0 (0–0) | same |
| process | max_rss_bytes | 22,589,440 (22,016,000–22,999,040) | 22,642,688 (22,298,624–24,535,040) | +0% (noise) |
