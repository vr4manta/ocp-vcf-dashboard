(function() {
    "use strict";

    var colors = {
        poolLine:    "#2b9af3",
        clusterLine: "#ec7a08",
        grid:        "#3c3f42",
        axes:        "#6a6e73",
        text:        "#a0a0a0",
        bg:          "#252525"
    };

    // Validated categorical palette (dark-mode steps), fixed order - see the
    // dataviz skill's references/palette.md. Reused across both network
    // charts so a given pool keeps the same color in each.
    var poolPalette = [
        "#3987e5", // blue
        "#d95926", // orange
        "#199e70", // aqua
        "#c98500", // yellow
        "#d55181", // magenta
        "#008300", // green
        "#9085e9", // violet
        "#e66767"  // red
    ];

    function initPoolCharts() {
        var container = document.getElementById("pool-charts");
        if (!container) return;
        if (typeof uPlot === "undefined") return;

        var poolName = container.dataset.pool;
        var range = container.dataset.range || "24h";
        fetchAndRenderPool(poolName, range);
    }

    function fetchAndRenderPool(poolName, range) {
        fetch("/api/graph/pool/" + encodeURIComponent(poolName) + "?range=" + range)
            .then(function(r) { return r.json(); })
            .then(function(data) {
                renderChart("cpu-chart", data.cpu, "CPU", "Pool Allocation", "Cluster Demand");
                renderChart("memory-chart", data.memory, "Memory", "Pool Allocation", "Cluster Demand");
            })
            .catch(function() {
                document.getElementById("cpu-chart").innerHTML = "<p class='chart-empty'>Failed to load chart data</p>";
                document.getElementById("memory-chart").innerHTML = "<p class='chart-empty'>Failed to load chart data</p>";
            });
    }

    function initNetworkUsageChart() {
        var container = document.getElementById("network-usage-charts");
        if (!container) return;
        if (typeof uPlot === "undefined") return;

        fetchAndRenderNetworkUsage(container.dataset.range || "24h");
    }

    function fetchAndRenderNetworkUsage(range) {
        fetch("/api/graph/networks?range=" + range)
            .then(function(r) { return r.json(); })
            .then(function(data) {
                var pools = data.pools || [];
                renderMultiSeriesChart("network-st-chart", data.timestamps, pools, "single_tenant_pct", "Single-Tenant Network");
                renderMultiSeriesChart("network-mt-chart", data.timestamps, pools, "multi_tenant_pct", "Multi-Tenant Network");
            })
            .catch(function() {
                ["network-st-chart", "network-mt-chart"].forEach(function(id) {
                    var el = document.getElementById(id);
                    if (el) el.innerHTML = "<p class='chart-empty'>Failed to load chart data</p>";
                });
            });
    }

    // renderMultiSeriesChart draws one line per pool, colored from the fixed
    // categorical palette so a pool keeps the same color across both the
    // single-tenant and multi-tenant charts.
    function renderMultiSeriesChart(elId, timestamps, pools, field, axisLabel) {
        var el = document.getElementById(elId);
        if (!el) return;

        el.innerHTML = "";

        var usable = pools.filter(function(p) { return p[field] && p[field].length > 0; });

        if (!timestamps || timestamps.length === 0 || usable.length === 0) {
            el.innerHTML = "<p class='chart-empty'>No data available</p>";
            return;
        }

        var chartData = [timestamps];
        var chartSeries = [{}];

        usable.forEach(function(p, i) {
            chartData.push(p[field]);
            chartSeries.push({
                label: p.pool_name,
                stroke: poolPalette[i % poolPalette.length],
                width: 2,
                points: { show: false }
            });
        });

        var width = el.clientWidth || 500;
        var height = 280;

        var opts = {
            width: width,
            height: height,
            series: chartSeries,
            axes: [
                {
                    stroke: colors.axes,
                    grid: { stroke: colors.grid, width: 1 },
                    ticks: { stroke: colors.grid, width: 1 },
                    font: "11px RedHatText, sans-serif",
                    labelFont: "12px RedHatText, sans-serif"
                },
                {
                    label: axisLabel + " Usage (%)",
                    labelSize: 20,
                    labelGap: 4,
                    stroke: colors.axes,
                    grid: { stroke: colors.grid, width: 1 },
                    ticks: { stroke: colors.grid, width: 1 },
                    font: "11px RedHatText, sans-serif",
                    labelFont: "12px RedHatText, sans-serif",
                    values: function(u, vals) {
                        return vals.map(function(v) { return v == null ? "" : v.toFixed(0) + "%"; });
                    }
                }
            ],
            scales: {
                y: { min: 0, max: 100 }
            },
            cursor: {
                drag: { x: false, y: false }
            },
            legend: {
                show: true
            }
        };

        new uPlot(opts, chartData, el);
    }

    function renderChart(elId, series, label, primaryLabel, secondaryLabel) {
        var el = document.getElementById(elId);
        if (!el) return;

        el.innerHTML = "";

        if (!series || !series.timestamps || series.timestamps.length === 0) {
            el.innerHTML = "<p class='chart-empty'>No data available</p>";
            return;
        }

        var hasPool = series.pool_used_pct && series.pool_used_pct.length > 0;
        var hasCluster = series.cluster_demand_pct && series.cluster_demand_pct.length > 0;

        if (!hasPool && !hasCluster) {
            el.innerHTML = "<p class='chart-empty'>No data available</p>";
            return;
        }

        var timestamps = series.timestamps;
        var chartData = [timestamps];
        var chartSeries = [{}];

        if (hasPool) {
            chartData.push(series.pool_used_pct);
            chartSeries.push({
                label: primaryLabel || "Pool Allocation",
                stroke: colors.poolLine,
                width: 2,
                points: { show: false }
            });
        }

        if (hasCluster) {
            chartData.push(series.cluster_demand_pct);
            chartSeries.push({
                label: secondaryLabel || "Cluster Demand",
                stroke: colors.clusterLine,
                width: 2,
                points: { show: false }
            });
        }

        var width = el.clientWidth || 500;
        var height = 280;

        var opts = {
            width: width,
            height: height,
            series: chartSeries,
            axes: [
                {
                    stroke: colors.axes,
                    grid: { stroke: colors.grid, width: 1 },
                    ticks: { stroke: colors.grid, width: 1 },
                    font: "11px RedHatText, sans-serif",
                    labelFont: "12px RedHatText, sans-serif"
                },
                {
                    label: label + " Utilization (%)",
                    labelSize: 20,
                    labelGap: 4,
                    stroke: colors.axes,
                    grid: { stroke: colors.grid, width: 1 },
                    ticks: { stroke: colors.grid, width: 1 },
                    font: "11px RedHatText, sans-serif",
                    labelFont: "12px RedHatText, sans-serif",
                    values: function(u, vals) {
                        return vals.map(function(v) { return v == null ? "" : v.toFixed(0) + "%"; });
                    }
                }
            ],
            scales: {
                y: { min: 0, max: 100 }
            },
            cursor: {
                drag: { x: false, y: false }
            },
            legend: {
                show: true
            }
        };

        new uPlot(opts, chartData, el);
    }

    function highlightActiveRange(range) {
        var buttons = document.querySelectorAll(".range-btn");
        for (var i = 0; i < buttons.length; i++) {
            if (buttons[i].dataset.range === range) {
                buttons[i].classList.add("active");
            } else {
                buttons[i].classList.remove("active");
            }
        }
    }

    document.addEventListener("click", function(e) {
        if (!e.target.classList.contains("range-btn")) return;
        var range = e.target.dataset.range;

        var poolContainer = document.getElementById("pool-charts");
        if (poolContainer) {
            poolContainer.dataset.range = range;
            fetchAndRenderPool(poolContainer.dataset.pool, range);
        }

        var networkContainer = document.getElementById("network-usage-charts");
        if (networkContainer) {
            networkContainer.dataset.range = range;
            fetchAndRenderNetworkUsage(range);
        }

        highlightActiveRange(range);
    });

    function init() {
        initPoolCharts();
        initNetworkUsageChart();
    }

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", init);
    } else {
        init();
    }
})();
