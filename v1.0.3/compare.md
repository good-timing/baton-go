## go: 1.0.2 published vs 1.0.3 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. The exception is a measure's 'limits': over 'repeats' runs of that measure alone, each named metric's median must be under its limit. scripts/check_bench_limit.py runs and checks them. A measure's 'noise_floor' gives, per metric and SDK, the change between runs of one version that is still noise. Median (lowest–highest) of 5, 5 runs.

CPU: 1.0.2 published: AMD EPYC 9V74 80-Core Processor (5); 1.0.3 checkout: AMD EPYC 9V74 80-Core Processor (5)

| measure | metric | 1.0.2 published | 1.0.3 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0552 (0.0548–0.0559) | 0.0553 (0.0537–0.0562) | +0% (noise) |
| overhead | plain_p99_ms | 0.299 (0.292–0.328) | 0.308 (0.297–0.335) | +3% (noise) |
| overhead | baton_p50_ms | 0.0839 (0.0829–0.0843) | 0.0833 (0.083–0.0835) | -1% (noise) |
| overhead | baton_p99_ms | 0.475 (0.469–0.48) | 0.473 (0.454–0.483) | -0% (noise) |
| overhead | added_p50_ms | 0.0287 (0.027–0.0291) | 0.0277 (0.0273–0.0297) | -3% (noise) |
| overhead | added_p99_ms | 0.176 (0.147–0.182) | 0.167 (0.146–0.176) | -5% (noise) |
| burst | plain_wall_ms | 811 (805–816) | 810 (804–846) | -0% (noise) |
| burst | plain_call_p50_ms | 0.744 (0.74–0.746) | 0.749 (0.737–0.786) | +1% |
| burst | plain_call_p99_ms | 1.74 (1.66–1.77) | 1.7 (1.66–1.76) | -2% (noise) |
| burst | wall_ms | 1,257 (1,238–1,337) | 1,240 (1,224–1,250) | -1% (noise) |
| burst | call_p50_ms | 1.21 (1.19–1.26) | 1.19 (1.18–1.2) | -1% |
| burst | call_p99_ms | 2.9 (2.78–3.66) | 2.74 (2.67–2.85) | -5% |
| burst | added_call_p50_ms | 0.465 (0.447–0.521) | 0.444 (0.403–0.454) | -4% |
| burst | added_call_p99_ms | 1.24 (1.05–1.97) | 0.984 (0.973–1.18) | -20% |
| burst | close_ms | 1.53 (1.28–1.78) | 1.59 (1.47–1.93) | +4% (noise) |
| burst | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| burst | delivered | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | plain_wall_ms | 832 (808–851) | 810 (808–813) | -3% (noise) |
| outage_hang | plain_call_p50_ms | 0.772 (0.747–0.781) | 0.748 (0.743–0.752) | -3% (noise) |
| outage_hang | plain_call_p99_ms | 1.73 (1.64–1.85) | 1.68 (1.62–1.7) | -3% (noise) |
| outage_hang | wall_ms | 947 (932–1,006) | 936 (925–954) | -1% (noise) |
| outage_hang | call_p50_ms | 0.962 (0.948–0.993) | 0.957 (0.938–0.966) | -1% (noise) |
| outage_hang | call_p99_ms | 2.13 (2.06–2.31) | 2.1 (2.06–2.14) | -2% (noise) |
| outage_hang | added_call_p50_ms | 0.201 (0.181–0.214) | 0.209 (0.19–0.223) | +4% (noise) |
| outage_hang | added_call_p99_ms | 0.422 (0.326–0.454) | 0.401 (0.371–0.516) | -5% (noise) |
| outage_hang | close_ms | 2,901 (2,900–2,901) | 2,900 (2,900–2,901) | -0% |
| outage_hang | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_down | plain_wall_ms | 813 (812–869) | 808 (802–814) | -1% |
| outage_down | plain_call_p50_ms | 0.752 (0.75–0.767) | 0.747 (0.74–0.752) | -1% |
| outage_down | plain_call_p99_ms | 1.75 (1.65–2.67) | 1.71 (1.69–1.73) | -2% (noise) |
| outage_down | wall_ms | 970 (962–1,038) | 971 (968–985) | +0% (noise) |
| outage_down | call_p50_ms | 0.966 (0.952–1.02) | 0.973 (0.966–0.977) | +1% (noise) |
| outage_down | call_p99_ms | 2.16 (2.15–2.4) | 2.19 (2.13–2.28) | +2% (noise) |
| outage_down | added_call_p50_ms | 0.214 (0.196–0.267) | 0.22 (0.219–0.237) | +3% (noise) |
| outage_down | added_call_p99_ms | 0.422 (-0.526–0.748) | 0.461 (0.418–0.555) | +0.0389 (noise) |
| outage_down | close_ms | 703 (703–703) | 703 (703–703) | +0% (noise) |
| outage_down | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| memory | heap_after_warmup_bytes | 1,057,800 (1,008,032–1,106,160) | 1,015,320 (976,456–1,047,208) | -4% (noise) |
| memory | heap_at_half_bytes | 1,045,352 (985,736–1,058,680) | 1,034,680 (975,488–1,067,544) | -1% (noise) |
| memory | heap_at_end_bytes | 1,052,440 (1,022,608–1,103,776) | 1,025,312 (662,080–1,061,872) | -3% (noise) |
| memory | growth_per_call_bytes | 0.709 (-0.852–5.01) | -0.567 (-32.7–4.41) | -1.28 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,331,432 (1,326,104–1,337,064) | 1,333,424 (1,321,288–1,337,320) | +0% (noise) |
| memory_outage | heap_at_half_bytes | 2,255,264 (2,245,128–2,262,920) | 2,255,624 (2,246,952–2,261,064) | +0% (noise) |
| memory_outage | heap_at_end_bytes | 2,257,528 (2,253,560–2,262,696) | 2,256,312 (2,249,832–2,258,232) | -0% (noise) |
| memory_outage | growth_per_call_bytes | 0.144 (-0.221–1.35) | -0.088 (-0.579–0.936) | -0.232 (noise) |
| large_result | plain_p50_ms | 0.935 (0.931–0.939) | 0.932 (0.93–0.938) | -0% (noise) |
| large_result | plain_p99_ms | 1.19 (1.17–1.23) | 1.18 (1.18–1.36) | -1% (noise) |
| large_result | baton_p50_ms | 1.4 (1.38–1.41) | 1.37 (1.36–1.38) | -2% |
| large_result | baton_p99_ms | 2.64 (2.61–3.02) | 2.82 (2.58–2.88) | +7% (noise) |
| large_result | added_p50_ms | 0.468 (0.446–0.475) | 0.435 (0.433–0.446) | -7% |
| large_result | added_p99_ms | 1.44 (1.41–1.84) | 1.52 (1.4–1.65) | +5% (noise) |
| large_result | heap_growth_bytes | 668,920 (491,888–967,472) | 841,568 (652,488–947,480) | +26% (noise) |
| large_result | emitted | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | delivered | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | lost | 0 (0–0) | 0 (0–0) | same |
| process | max_rss_bytes | 22,462,464 (22,282,240–23,261,184) | 22,343,680 (22,224,896–22,867,968) | -1% (noise) |
