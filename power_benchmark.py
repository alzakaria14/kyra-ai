#!/usr/bin/env python3
"""Benchmark konsumsi daya Kyra -> Ollama tanpa WhatsApp gateway.

Membutuhkan Python 3.9+ saja (tanpa package tambahan), server Kyra yang sudah
direbuild, Ollama, dan LibreHardwareMonitor Web Server pada port 8085.
"""
import argparse
import csv
import json
import math
import re
import statistics
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


NUMBER = re.compile(r"[-+]?\d+(?:\.\d+)?")


def get_json(url, timeout=10):
    with urlopen(url, timeout=timeout) as response:
        return json.loads(response.read().decode("utf-8"))


def post_json(url, payload, timeout):
    request = Request(url, data=json.dumps(payload).encode("utf-8"),
                      headers={"Content-Type": "application/json"}, method="POST")
    with urlopen(request, timeout=timeout) as response:
        return json.loads(response.read().decode("utf-8"))


def number(value):
    match = NUMBER.search(str(value))
    return float(match.group()) if match else None


def walk_sensors(node, hardware=""):
    hardware = node.get("Text", hardware) if node.get("HardwareId") else hardware
    if node.get("SensorId"):
        value = number(node.get("RawValue", node.get("Value", "")))
        if value is not None:
            yield {"hardware": hardware, "name": node.get("Text", ""),
                   "type": node.get("Type", ""), "value": value,
                   "sensor_id": node["SensorId"]}
    for child in node.get("Children", []):
        yield from walk_sensors(child, hardware)


def choose(sensors, hardware_part, sensor_type, preferred_names):
    candidates = [s for s in sensors if hardware_part in s["sensor_id"].lower()
                  and s["type"].lower() == sensor_type.lower()]
    for wanted in preferred_names:
        for sensor in candidates:
            if sensor["name"].lower() == wanted.lower():
                return sensor
    return candidates[0] if candidates else None


def sample_lhm(lhm_url):
    sensors = list(walk_sensors(get_json(lhm_url)))
    selected = {
        "cpu_power_w": choose(sensors, "cpu", "Power", ["CPU Package", "Package"]),
        "gpu_power_w": choose(sensors, "gpu", "Power", ["GPU Package", "GPU Power", "Total Board Power"]),
        "cpu_temp_c": choose(sensors, "cpu", "Temperature", ["CPU Package", "Core Average", "Core Max"]),
        "gpu_temp_c": choose(sensors, "gpu", "Temperature", ["GPU Core", "GPU Hot Spot"]),
        "cpu_load_pct": choose(sensors, "cpu", "Load", ["CPU Total"]),
        "gpu_load_pct": choose(sensors, "gpu", "Load", ["GPU Core"]),
        "cpu_clock_mhz": choose(sensors, "cpu", "Clock", ["CPU Core #1"]),
        "gpu_clock_mhz": choose(sensors, "gpu", "Clock", ["GPU Core"]),
        "gpu_fan_rpm": choose(sensors, "gpu", "Fan", ["GPU Fan 1"]),
    }
    result = {"timestamp_utc": datetime.now(timezone.utc).isoformat(), "monotonic_s": time.monotonic()}
    result.update({key: (sensor["value"] if sensor else None) for key, sensor in selected.items()})
    result["sensor_sources"] = {key: (sensor["sensor_id"] if sensor else None) for key, sensor in selected.items()}
    return result


class Monitor(threading.Thread):
    def __init__(self, url, interval):
        super().__init__(daemon=True)
        self.url, self.interval, self.stop_event, self.samples, self.errors = url, interval, threading.Event(), [], []

    def run(self):
        next_tick = time.monotonic()
        while not self.stop_event.is_set():
            try:
                self.samples.append(sample_lhm(self.url))
            except Exception as exc:  # telemetry failure must not silently stop benchmark
                self.errors.append(f"{datetime.now(timezone.utc).isoformat()}: {exc}")
            next_tick += self.interval
            self.stop_event.wait(max(0, next_tick - time.monotonic()))


def values(samples, key):
    return [s[key] for s in samples if s.get(key) is not None]


def mean(samples, key):
    vals = values(samples, key)
    return statistics.fmean(vals) if vals else None


def maximum(samples, key):
    vals = values(samples, key)
    return max(vals) if vals else None


def energy_wh(samples, key):
    """Integral daya trapezoid; None berarti sensor tidak tersedia."""
    rows = [s for s in samples if s.get(key) is not None]
    if len(rows) < 2:
        return None
    watt_seconds = sum((a[key] + b[key]) / 2 * (b["monotonic_s"] - a["monotonic_s"])
                       for a, b in zip(rows, rows[1:]))
    return watt_seconds / 3600


def rounded(value, digits=3):
    return round(value, digits) if value is not None and math.isfinite(value) else None


def write_samples(path, samples):
    fields = ["phase", "timestamp_utc", "monotonic_s", "cpu_power_w", "gpu_power_w", "cpu_temp_c", "gpu_temp_c",
              "cpu_load_pct", "gpu_load_pct", "cpu_clock_mhz", "gpu_clock_mhz", "gpu_fan_rpm"]
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for sample in samples:
            writer.writerow({field: sample.get(field) for field in fields})


def main():
    parser = argparse.ArgumentParser(description="Uji daya Kyra -> Ollama langsung, tanpa WhatsApp gateway.")
    parser.add_argument("--count", type=int, default=1000, help="jumlah pesan (default: 1000)")
    parser.add_argument("--message", default="Halo", help="isi pesan (harus relevan untuk Kyra)")
    parser.add_argument("--kyra-url", default="http://127.0.0.1:8080/benchmark/message")
    parser.add_argument("--lhm-url", default="http://127.0.0.1:8085/data.json")
    parser.add_argument("--interval", type=float, default=0.5, help="interval sampel LHM dalam detik")
    parser.add_argument("--baseline-seconds", type=float, default=10, help="durasi idle sebelum beban")
    parser.add_argument("--timeout", type=float, default=120, help="timeout per pesan dalam detik")
    parser.add_argument("--output-dir", default="benchmark_results")
    args = parser.parse_args()
    if args.count < 1 or args.interval <= 0 or args.baseline_seconds < 0:
        parser.error("count minimal 1; interval harus > 0; baseline-seconds tidak boleh negatif")

    # Fail fast: memastikan API LHM dan endpoint Kyra tersedia sebelum beban dimulai.
    first = sample_lhm(args.lhm_url)
    try:
        post_json(args.kyra_url, {"message": args.message}, args.timeout)
    except HTTPError as exc:
        raise SystemExit(f"Endpoint Kyra menolak request ({exc.code}): {exc.read().decode(errors='replace')}")
    except URLError as exc:
        raise SystemExit(f"Endpoint Kyra tidak dapat dihubungi: {exc.reason}")
    print("Pemanasan 1 request selesai. Memulai pengambilan baseline...")

    monitor = Monitor(args.lhm_url, args.interval)
    monitor.start()
    time.sleep(args.baseline_seconds)
    workload_start = time.monotonic()
    start_index = len(monitor.samples)
    latencies, failures = [], []
    for index in range(1, args.count + 1):
        started = time.monotonic()
        try:
            post_json(args.kyra_url, {"message": args.message}, args.timeout)
            latencies.append(time.monotonic() - started)
        except Exception as exc:
            failures.append({"message_number": index, "error": str(exc), "latency_s": time.monotonic() - started})
        if index == 1 or index % 10 == 0 or index == args.count:
            print(f"{index}/{args.count} | sukses={len(latencies)} gagal={len(failures)}")
    workload_end = time.monotonic()
    end_index = len(monitor.samples)
    monitor.stop_event.set()
    monitor.join(timeout=args.interval * 3 + 2)

    # Sampel dibagi berdasarkan momen yang dicatat; baseline tidak termasuk warm-up.
    baseline = [s for s in monitor.samples[:start_index] if s["monotonic_s"] < workload_start]
    workload = [s for s in monitor.samples[start_index:end_index]
                if workload_start <= s["monotonic_s"] <= workload_end]
    for sample in baseline: sample["phase"] = "baseline"
    for sample in workload: sample["phase"] = "workload"
    output = Path(args.output_dir) / datetime.now().strftime("%Y%m%d_%H%M%S")
    output.mkdir(parents=True, exist_ok=True)
    write_samples(output / "telemetry.csv", baseline + workload)
    (output / "failures.json").write_text(json.dumps(failures, indent=2), encoding="utf-8")

    def metric(key):
        base, active = mean(baseline, key), mean(workload, key)
        return {"baseline_mean": rounded(base), "workload_mean": rounded(active),
                "increase_from_baseline": rounded(active - base) if active is not None and base is not None else None,
                "workload_max": rounded(maximum(workload, key)), "workload_energy_wh": rounded(energy_wh(workload, key), 6),
                "energy_per_successful_message_wh": rounded(energy_wh(workload, key) / len(latencies), 8) if latencies and energy_wh(workload, key) is not None else None}

    duration = workload_end - workload_start
    summary = {"started_utc": datetime.now(timezone.utc).isoformat(), "configuration": vars(args),
               "sensor_sources": first["sensor_sources"], "requests": {"requested": args.count, "successful": len(latencies), "failed": len(failures)},
               "duration_seconds": rounded(duration, 3), "throughput_messages_per_second": rounded(len(latencies) / duration, 4),
               "latency_seconds": {"mean": rounded(statistics.fmean(latencies), 3) if latencies else None,
                                   "p50": rounded(statistics.median(latencies), 3) if latencies else None,
                                   "min": rounded(min(latencies), 3) if latencies else None, "max": rounded(max(latencies), 3) if latencies else None},
               "power_cpu_w": metric("cpu_power_w"), "power_gpu_w": metric("gpu_power_w"),
               "temperature_cpu_c": metric("cpu_temp_c"), "temperature_gpu_c": metric("gpu_temp_c"),
               "cpu_load_pct": metric("cpu_load_pct"), "gpu_load_pct": metric("gpu_load_pct"),
               "cpu_clock_mhz": metric("cpu_clock_mhz"), "gpu_clock_mhz": metric("gpu_clock_mhz"),
               "gpu_fan_rpm": metric("gpu_fan_rpm"), "telemetry": {"baseline_samples": len(baseline), "workload_samples": len(workload), "errors": monitor.errors}}
    (output / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False), encoding="utf-8")
    print(json.dumps(summary, indent=2, ensure_ascii=False))
    print(f"\nFile analisis: {output / 'summary.json'} dan {output / 'telemetry.csv'}")
    if not workload or monitor.errors:
        raise SystemExit("Benchmark selesai, tetapi telemetri tidak lengkap; lihat summary.json.")


if __name__ == "__main__":
    main()
