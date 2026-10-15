#!/usr/bin/env bash
# readiness-overlap-monitor.sh — P1-32 staging fence validation scaffold
# (hardened in P1-33).
#
# Polls the /readyz endpoint of every gateway instance given, repeatedly, and
# records which instances answered HTTP 200 in each sampling round. It reports
# every round in which MORE THAN ONE instance was ready ("overlap"), and the
# longest run of rounds in which NO instance was ready (availability gap).
#
# READ-ONLY: it only issues HTTP GETs to health endpoints. It never touches a
# database, a carrier or any configuration, and prints no secrets (readiness
# responses carry none; URLs are not written to the CSV or the summary).
#
# LIMITS — read before relying on a result:
#   * Sampling is SEQUENTIAL: within one round, instances are queried one after
#     another, so a round spans the time of N requests. An overlap shorter than
#     the sampling period can be MISSED, and an ownership hand-over that
#     happens BETWEEN two requests of one round could be reported as a false
#     overlap. Every reported overlap must be checked against the instances'
#     own fence logs (acquired / LOST timestamps). The query order alternates
#     each round to avoid a systematic bias.
#   * The sampling PERIOD is the round's own duration PLUS the interval (the
#     interval is a pause between rounds, not a fixed rate). The summary
#     reports the measured round span and period: compare them with the
#     required resolution before accepting a run.
#   * A sample with no HTTP response (connection refused, timeout) is
#     UNOBSERVED, not "not ready" evidence. They are counted and reported.
#   * Zero observed overlaps in a finite run is evidence, not proof: it cannot
#     show safety under every failure or split-brain scenario.
#
# Requirements: bash, curl, awk, GNU date (millisecond `date +%s%3N`).
#
# Usage:
#   readiness-overlap-monitor.sh -d SECONDS -i MILLISECONDS -o OUT.csv URL URL [URL ...]
#     -d  run duration in seconds (>= 1); the run ends after the round in
#         progress when the duration elapses
#     -i  pause between rounds in milliseconds (>= 1)
#     -o  CSV output path; must NOT already exist (evidence is never overwritten)
#     URL full readiness URL, e.g. http://gw-a.staging.internal:8081/readyz
#   Exit status:
#     0  conclusive: no overlap observed, every instance answered at least once
#     1  at least one overlap round observed (takes precedence over 3)
#     2  usage or environment error (nothing sampled)
#     3  INCONCLUSIVE: zero rounds, or an instance never returned any HTTP
#        response — the run cannot vouch for anything
set -euo pipefail

usage() {
  echo "usage: $0 -d SECONDS -i MILLISECONDS -o OUT.csv URL URL [URL ...]  (at least two instances)" >&2
  exit 2
}

duration=""; interval_ms=""; out=""
while getopts "d:i:o:" opt; do
  case "$opt" in
    d) duration="$OPTARG" ;;
    i) interval_ms="$OPTARG" ;;
    o) out="$OPTARG" ;;
    *) usage ;;
  esac
done
shift $((OPTIND - 1))
if [ -z "$duration" ] || [ -z "$interval_ms" ] || [ -z "$out" ] || [ "$#" -lt 2 ]; then
  usage
fi
case "$duration$interval_ms" in *[!0-9]*) echo "duration and interval must be integers" >&2; exit 2 ;; esac
if [ "$duration" -lt 1 ] || [ "$interval_ms" -lt 1 ]; then
  echo "duration must be >= 1 second and interval >= 1 millisecond" >&2
  exit 2
fi
for tool in curl awk date; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 2
  fi
done
case "$(date +%s%3N)" in
  ''|*[!0-9]*) echo "date +%s%3N must print epoch milliseconds (GNU date required)" >&2; exit 2 ;;
esac
if [ -e "$out" ]; then
  echo "output file already exists; refusing to overwrite evidence: $out" >&2
  exit 2
fi

urls=("$@")
n=${#urls[@]}
# A request must finish well inside one sampling interval.
max_time=$(awk -v ms="$interval_ms" -v n="$n" 'BEGIN { t = ms / 1000 / (2 * n); if (t < 0.05) t = 0.05; printf "%.3f", t }')
sleep_s=$(awk -v ms="$interval_ms" 'BEGIN { printf "%.3f", ms / 1000 }')

now_ms() { date +%s%3N; }

{
  printf 'round,t_start_ms,t_end_ms,ready_count'
  for i in $(seq 0 $((n - 1))); do printf ',i%d_code' "$i"; done
  printf '\n'
} > "$out"

responses=(); for i in $(seq 0 $((n - 1))); do responses[$i]=0; done
end=$(( $(now_ms) + duration * 1000 ))
round=0; overlaps=0; longest_gap=0; gap=0; rounds_all_unready=0; unobserved=0
span_sum=0; span_max=0; first_start=0; last_start=0
while [ "$(now_ms)" -lt "$end" ]; do
  t0=$(now_ms)
  [ "$round" -eq 0 ] && first_start=$t0
  last_start=$t0
  codes=()
  for i in $(seq 0 $((n - 1))); do codes[$i]=000; done
  # Alternate the query order each round.
  if [ $((round % 2)) -eq 0 ]; then order=$(seq 0 $((n - 1))); else order=$(seq $((n - 1)) -1 0); fi
  for i in $order; do
    c=$(curl -s -o /dev/null -w '%{http_code}' --max-time "$max_time" "${urls[$i]}" || true)
    codes[$i]=${c:-000}
  done
  t1=$(now_ms)
  ready=0
  for i in $(seq 0 $((n - 1))); do
    case "${codes[$i]}" in
      200) ready=$((ready + 1)); responses[$i]=$((responses[$i] + 1)) ;;
      000) unobserved=$((unobserved + 1)) ;;
      *) responses[$i]=$((responses[$i] + 1)) ;;
    esac
  done
  { printf '%d,%d,%d,%d' "$round" "$t0" "$t1" "$ready"; for i in $(seq 0 $((n - 1))); do printf ',%s' "${codes[$i]}"; done; printf '\n'; } >> "$out"
  if [ "$ready" -gt 1 ]; then overlaps=$((overlaps + 1)); fi
  if [ "$ready" -eq 0 ]; then
    gap=$((gap + 1)); rounds_all_unready=$((rounds_all_unready + 1))
    if [ "$gap" -gt "$longest_gap" ]; then longest_gap=$gap; fi
  else
    gap=0
  fi
  span=$((t1 - t0)); span_sum=$((span_sum + span))
  if [ "$span" -gt "$span_max" ]; then span_max=$span; fi
  round=$((round + 1))
  sleep "$sleep_s"
done

span_avg=0; period_avg=0
if [ "$round" -gt 0 ]; then span_avg=$((span_sum / round)); fi
if [ "$round" -gt 1 ]; then period_avg=$(( (last_start - first_start) / (round - 1) )); fi
silent=""
for i in $(seq 0 $((n - 1))); do
  if [ "${responses[$i]}" -eq 0 ]; then silent="$silent i$i"; fi
done

verdict="no_overlap_observed"; status=0
if [ "$round" -eq 0 ] || [ -n "$silent" ]; then verdict="INCONCLUSIVE"; status=3; fi
if [ "$overlaps" -gt 0 ]; then verdict="OVERLAP_OBSERVED"; status=1; fi

echo "verdict=$verdict rounds=$round overlap_rounds=$overlaps rounds_with_no_ready_instance=$rounds_all_unready longest_unready_run_rounds=$longest_gap unobserved_samples=$unobserved instances_never_answering=${silent:- none} round_span_ms_avg=$span_avg round_span_ms_max=$span_max sampling_period_ms_avg=$period_avg interval_ms=$interval_ms instances=$n csv=$out"
exit "$status"
