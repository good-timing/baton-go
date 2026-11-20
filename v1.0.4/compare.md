## go: 1.0.3 published vs 1.0.4 checkout

The numbers compare two versions of one SDK on one machine. They are not what a vendor sees in production: the collector is local and the client is in the same process. The exception is a measure's 'limits': over 'repeats' runs of that measure alone, each named metric's median must be under its limit. scripts/check_bench_limit.py runs and checks them. A measure's 'noise_floor' gives, per metric and SDK, the change between runs of one version that is still noise. Median (lowest–highest) of 5, 5 runs.

CPU: 1.0.3 published: INTEL(R) XEON(R) PLATINUM 8573C (5); 1.0.4 checkout: INTEL(R) XEON(R) PLATINUM 8573C (5)

| measure | metric | 1.0.3 published | 1.0.4 checkout | change |
|---|---|---|---|---|
| overhead | plain_p50_ms | 0.0765 (0.0742–0.0793) | 0.075 (0.0745–0.0762) | -2% (noise) |
| overhead | plain_p99_ms | 0.379 (0.36–0.393) | 0.372 (0.365–0.401) | -2% (noise) |
| overhead | baton_p50_ms | 0.0963 (0.0944–0.101) | 0.0952 (0.0941–0.118) | -1% (noise) |
| overhead | baton_p99_ms | 0.55 (0.523–0.565) | 0.563 (0.524–0.614) | +2% (noise) |
| overhead | added_p50_ms | 0.0198 (0.017–0.0237) | 0.0205 (0.019–0.0432) | +4% (noise) |
| overhead | added_p99_ms | 0.17 (0.15–0.202) | 0.169 (0.153–0.219) | -1% (noise) |
| burst | plain_wall_ms | 962 (955–972) | 960 (951–985) | -0% (noise) |
| burst | plain_call_p50_ms | 0.893 (0.882–0.902) | 0.888 (0.881–0.907) | -1% (noise) |
| burst | plain_call_p99_ms | 1.98 (1.93–2.02) | 2.04 (1.95–2.18) | +3% |
| burst | wall_ms | 1,456 (1,451–1,475) | 1,462 (1,451–1,482) | +0% (noise) |
| burst | call_p50_ms | 1.4 (1.39–1.42) | 1.4 (1.39–1.41) | -0% (noise) |
| burst | call_p99_ms | 3.19 (3.15–3.25) | 3.25 (3.17–3.34) | +2% (noise) |
| burst | added_call_p50_ms | 0.506 (0.498–0.533) | 0.508 (0.486–0.525) | +0% (noise) |
| burst | added_call_p99_ms | 1.24 (1.15–1.27) | 1.21 (1.15–1.35) | -3% (noise) |
| burst | close_ms | 1.86 (1.62–2.22) | 1.83 (1.64–2.03) | -2% (noise) |
| burst | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| burst | delivered | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| burst | lost | 0 (0–0) | 0 (0–0) | same |
| outage_hang | plain_wall_ms | 958 (956–974) | 965 (957–996) | +1% (noise) |
| outage_hang | plain_call_p50_ms | 0.898 (0.886–0.905) | 0.892 (0.886–0.917) | -1% (noise) |
| outage_hang | plain_call_p99_ms | 1.96 (1.93–2.05) | 2.01 (1.9–2.14) | +3% (noise) |
| outage_hang | wall_ms | 1,200 (1,192–1,210) | 1,196 (1,184–1,211) | -0% (noise) |
| outage_hang | call_p50_ms | 1.23 (1.23–1.25) | 1.23 (1.22–1.24) | -0% (noise) |
| outage_hang | call_p99_ms | 2.62 (2.58–2.69) | 2.63 (2.61–2.71) | +0% (noise) |
| outage_hang | added_call_p50_ms | 0.341 (0.321–0.361) | 0.333 (0.323–0.342) | -3% (noise) |
| outage_hang | added_call_p99_ms | 0.642 (0.565–0.732) | 0.615 (0.568–0.713) | -4% (noise) |
| outage_hang | close_ms | 2,901 (2,901–2,901) | 2,901 (2,901–2,901) | +0% (noise) |
| outage_hang | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_hang | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_hang | lost | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_down | plain_wall_ms | 968 (958–981) | 964 (951–968) | -0% (noise) |
| outage_down | plain_call_p50_ms | 0.899 (0.89–0.907) | 0.896 (0.885–0.906) | -0% (noise) |
| outage_down | plain_call_p99_ms | 2 (1.93–2.08) | 1.96 (1.92–2.02) | -2% (noise) |
| outage_down | wall_ms | 1,238 (1,227–1,352) | 1,226 (1,210–1,234) | -1% |
| outage_down | call_p50_ms | 1.24 (1.23–1.33) | 1.23 (1.23–1.24) | -1% |
| outage_down | call_p99_ms | 2.67 (2.64–3.11) | 2.63 (2.54–2.7) | -1% |
| outage_down | added_call_p50_ms | 0.343 (0.336–0.42) | 0.337 (0.327–0.348) | -2% (noise) |
| outage_down | added_call_p99_ms | 0.668 (0.661–1.02) | 0.644 (0.619–0.738) | -4% |
| outage_down | close_ms | 703 (703–703) | 703 (703–704) | +0% (noise) |
| outage_down | emitted | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| outage_down | delivered | 0 (0–0) | 0 (0–0) | same |
| outage_down | lost | 20,400 (20,400–20,400) | 20,400 (20,400–20,400) | same |
| memory | heap_after_warmup_bytes | 1,048,904 (1,022,344–1,060,056) | 1,050,592 (1,021,480–1,059,792) | +0% (noise) |
| memory | heap_at_half_bytes | 1,023,688 (1,020,416–1,062,696) | 1,060,920 (1,021,480–1,136,872) | +4% (noise) |
| memory | heap_at_end_bytes | 1,028,904 (1,018,568–1,060,584) | 1,063,240 (1,060,016–1,065,256) | +3% |
| memory | growth_per_call_bytes | 0.343 (-3.38–3.94) | 0.434 (-7.69–3.98) | +0.0904 (noise) |
| memory_outage | heap_after_warmup_bytes | 1,332,648 (1,328,712–1,338,672) | 1,336,936 (1,333,768–1,341,000) | +0% (noise) |
| memory_outage | heap_at_half_bytes | 2,261,096 (2,251,208–2,267,064) | 2,258,648 (2,255,240–2,259,096) | -0% (noise) |
| memory_outage | heap_at_end_bytes | 2,256,168 (2,250,216–2,269,544) | 2,260,376 (2,255,608–2,264,680) | +0% (noise) |
| memory_outage | growth_per_call_bytes | -0.194 (-0.53–0.248) | 0.427 (-0.139–0.571) | +0.621 (noise) |
| large_result | plain_p50_ms | 0.976 (0.972–1.04) | 0.98 (0.976–0.99) | +0% (noise) |
| large_result | plain_p99_ms | 1.29 (1.27–1.42) | 1.28 (1.26–1.41) | -1% (noise) |
| large_result | baton_p50_ms | 1.44 (1.41–1.48) | 1.45 (1.43–1.46) | +0% (noise) |
| large_result | baton_p99_ms | 2.42 (2.19–2.61) | 2.26 (2.17–2.38) | -7% (noise) |
| large_result | added_p50_ms | 0.468 (0.435–0.474) | 0.46 (0.452–0.474) | -2% (noise) |
| large_result | added_p99_ms | 1.05 (0.91–1.32) | 0.968 (0.879–1.07) | -7% (noise) |
| large_result | heap_growth_bytes | 461,096 (398,656–680,480) | 620,336 (488,816–739,480) | +35% (noise) |
| large_result | emitted | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | delivered | 2,200 (2,200–2,200) | 2,200 (2,200–2,200) | same |
| large_result | lost | 0 (0–0) | 0 (0–0) | same |
| process | max_rss_bytes | 22,638,592 (21,929,984–27,754,496) | 22,474,752 (22,372,352–24,637,440) | -1% (noise) |
