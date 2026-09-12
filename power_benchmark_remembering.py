#!/usr/bin/env python3
"""Benchmark daya Kyra dengan remembering dan status pesanan dari MySQL.

Tidak melewati WhatsApp gateway. Request pemanasan membuat/memperbarui fixture
benchmark khusus; request workload hanya membaca customer, remembering, riwayat,
dan status pesanan dari MySQL sebelum memanggil Ollama.
"""
import argparse
import json
import math
import statistics
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError

from power_benchmark import Monitor, integral_unit_hours, maximum, mean, post_json, sample_lhm, write_samples


def rounded(value, digits=3):
    return round(value, digits) if value is not None and math.isfinite(value) else None


def main():
    parser = argparse.ArgumentParser(
        description="Uji daya Kyra -> MySQL remembering/status pesanan -> Ollama, tanpa WhatsApp gateway."
    )
    parser.add_argument("--count", type=int, default=1000, help="jumlah pesan (default: 1000)")
    parser.add_argument(
        "--message", default="Bagaimana status pesanan saya?",
        help="pesan pertanyaan status pesanan yang dikirim pada setiap request",
    )
    parser.add_argument("--kyra-url", default="http://127.0.0.1:8080/benchmark/remembering")
    parser.add_argument("--lhm-url", default="http://127.0.0.1:8085/data.json")
    parser.add_argument("--interval", type=float, default=0.5, help="interval sampel LHM dalam detik")
    parser.add_argument("--baseline-seconds", type=float, default=10, help="durasi idle sebelum beban")
    parser.add_argument("--timeout", type=float, default=120, help="timeout per pesan dalam detik")
    parser.add_argument("--output-dir", default="benchmark_results_remembering")
    args = parser.parse_args()
    if args.count < 1 or args.interval <= 0 or args.baseline_seconds < 0:
        parser.error("count minimal 1; interval harus > 0; baseline-seconds tidak boleh negatif")
    if not args.message.strip():
        parser.error("message tidak boleh kosong")

    first = sample_lhm(args.lhm_url)
    try:
        warmup = post_json(args.kyra_url, {"message": args.message, "setup": True}, args.timeout)
    except HTTPError as exc:
        raise SystemExit(f"Endpoint Kyra menolak request ({exc.code}): {exc.read().decode(errors='replace')}")
    except URLError as exc:
        raise SystemExit(f"Endpoint Kyra tidak dapat dihubungi: {exc.reason}")
    if warmup.get("memory_source") != "mysql" or not warmup.get("order_id"):
        raise SystemExit("Endpoint tidak mengonfirmasi remembering dan pesanan dari MySQL.")
    print(
        "Pemanasan dan setup fixture selesai "
        f"(customer={warmup['customer_id']}, order={warmup['order_id']}, status={warmup['order_status']})."
    )
    print("Memulai pengambilan baseline...")

    monitor = Monitor(args.lhm_url, args.interval)
    monitor.start()
    time.sleep(args.baseline_seconds)
    workload_start = time.monotonic()
    start_index = len(monitor.samples)
    latencies, failures = [], []
    observed_orders = set()
    for index in range(1, args.count + 1):
        started = time.monotonic()
        try:
            result = post_json(args.kyra_url, {"message": args.message, "setup": False}, args.timeout)
            if result.get("memory_source") != "mysql" or not result.get("order_id"):
                raise RuntimeError("response tidak memuat bukti remembering MySQL")
            observed_orders.add((result["order_id"], result.get("order_status")))
            latencies.append(time.monotonic() - started)
        except Exception as exc:
            failures.append({"message_number": index, "error": str(exc), "latency_s": time.monotonic() - started})
        if index == 1 or index % 10 == 0 or index == args.count:
            print(f"{index}/{args.count} | sukses={len(latencies)} gagal={len(failures)}")
    workload_end = time.monotonic()
    end_index = len(monitor.samples)
    monitor.stop_event.set()
    monitor.join(timeout=args.interval * 3 + 2)

    baseline = [s for s in monitor.samples[:start_index] if s["monotonic_s"] < workload_start]
    workload = [s for s in monitor.samples[start_index:end_index]
                if workload_start <= s["monotonic_s"] <= workload_end]
    for sample in baseline:
        sample["phase"] = "baseline"
    for sample in workload:
        sample["phase"] = "workload"
    output = Path(args.output_dir) / datetime.now().strftime("%Y%m%d_%H%M%S")
    output.mkdir(parents=True, exist_ok=True)
    write_samples(output / "telemetry.csv", baseline + workload)
    (output / "failures.json").write_text(json.dumps(failures, indent=2), encoding="utf-8")

    def metric(key, unit, is_power=False):
        base, active = mean(baseline, key), mean(workload, key)
        integral = integral_unit_hours(workload, key)
        integral_key = "workload_energy_wh" if is_power else f"workload_{unit}_hours"
        per_message_key = "energy_per_successful_message_wh" if is_power else f"{unit}_hours_per_successful_message"
        return {
            "baseline_mean": rounded(base), "workload_mean": rounded(active),
            "increase_from_baseline": rounded(active - base) if active is not None and base is not None else None,
            "workload_max": rounded(maximum(workload, key)), integral_key: rounded(integral, 6),
            per_message_key: rounded(integral / len(latencies), 8) if latencies and integral is not None else None,
        }

    duration = workload_end - workload_start
    summary = {
        "started_utc": datetime.now(timezone.utc).isoformat(), "configuration": vars(args),
        "fixture": {"customer_id": warmup["customer_id"], "memory_source": "mysql",
                    "observed_orders": [{"order_id": order_id, "status": status}
                                        for order_id, status in sorted(observed_orders)]},
        "sensor_sources": first["sensor_sources"],
        "requests": {"requested": args.count, "successful": len(latencies), "failed": len(failures)},
        "duration_seconds": rounded(duration, 3),
        "throughput_messages_per_second": rounded(len(latencies) / duration, 4),
        "latency_seconds": {
            "mean": rounded(statistics.fmean(latencies), 3) if latencies else None,
            "p50": rounded(statistics.median(latencies), 3) if latencies else None,
            "min": rounded(min(latencies), 3) if latencies else None,
            "max": rounded(max(latencies), 3) if latencies else None,
        },
        "power_cpu_w": metric("cpu_power_w", "w", True), "power_gpu_w": metric("gpu_power_w", "w", True),
        "temperature_cpu_c": metric("cpu_temp_c", "c"), "temperature_gpu_c": metric("gpu_temp_c", "c"),
        "cpu_load_pct": metric("cpu_load_pct", "pct"), "gpu_load_pct": metric("gpu_load_pct", "pct"),
        "cpu_clock_mhz": metric("cpu_clock_mhz", "mhz"), "gpu_clock_mhz": metric("gpu_clock_mhz", "mhz"),
        "gpu_fan_rpm": metric("gpu_fan_rpm", "rpm"),
        "telemetry": {"baseline_samples": len(baseline), "workload_samples": len(workload), "errors": monitor.errors},
    }
    (output / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False), encoding="utf-8")
    print(json.dumps(summary, indent=2, ensure_ascii=False))
    print(f"\nFile analisis: {output / 'summary.json'} dan {output / 'telemetry.csv'}")
    if not workload or monitor.errors:
        raise SystemExit("Benchmark selesai, tetapi telemetri tidak lengkap; lihat summary.json.")


if __name__ == "__main__":
    main()
