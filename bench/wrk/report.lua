local output_path = os.getenv("WRK_JSON_PATH")

function done(summary, latency, requests)
  if output_path == nil or output_path == "" then
    error("WRK_JSON_PATH must name the structured result file")
  end

  local file, open_error = io.open(output_path, "w")
  if file == nil then
    error("open WRK_JSON_PATH: " .. tostring(open_error))
  end

  local document = string.format(
    '{"schema_version":"wrk-1","requests":%d,"bytes":%d,"duration_us":%d,' ..
    '"requests_per_second":%.9f,"transfer_bytes_per_second":%.9f,' ..
    '"p50_us":%.9f,"p95_us":%.9f,"p99_us":%.9f,' ..
    '"errors":{"connect":%d,"read":%d,"write":%d,"timeout":%d},"non_2xx":%d}\n',
    summary.requests,
    summary.bytes,
    summary.duration,
    summary.requests / (summary.duration / 1000000),
    summary.bytes / (summary.duration / 1000000),
    latency:percentile(50.0),
    latency:percentile(95.0),
    latency:percentile(99.0),
    summary.errors.connect,
    summary.errors.read,
    summary.errors.write,
    summary.errors.timeout,
    summary.errors.status
  )
  file:write(document)
  file:close()
end
