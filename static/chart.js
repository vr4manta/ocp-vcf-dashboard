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

    function initPoolCharts() {
        var container = document.getElementById("pool-charts");
        if (!container) return;
        if (typeof uPlot === "undefined") return;

        var poolName = container.dataset.pool;
        var range = container.dataset.range || "24h";
        fetchAndRender(poolName, range);
    }

    function fetchAndRender(poolName, range) {
        fetch("/api/graph/pool/" + encodeURIComponent(poolName) + "?range=" + range)
            .then(function(r) { return r.json(); })
            .then(function(data) {
                renderChart("cpu-chart", data.cpu, "CPU");
                renderChart("memory-chart", data.memory, "Memory");
                highlightActiveRange(range);
            })
            .catch(function() {
                document.getElementById("cpu-chart").innerHTML = "<p class='chart-empty'>Failed to load chart data</p>";
                document.getElementById("memory-chart").innerHTML = "<p class='chart-empty'>Failed to load chart data</p>";
            });
    }

    function renderChart(elId, series, label) {
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
                label: "Pool Allocation",
                stroke: colors.poolLine,
                width: 2,
                points: { show: false }
            });
        }

        if (hasCluster) {
            chartData.push(series.cluster_demand_pct);
            chartSeries.push({
                label: "Cluster Demand",
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
        if (e.target.classList.contains("range-btn")) {
            var range = e.target.dataset.range;
            var container = document.getElementById("pool-charts");
            if (!container) return;
            container.dataset.range = range;
            fetchAndRender(container.dataset.pool, range);
        }
    });

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", initPoolCharts);
    } else {
        initPoolCharts();
    }
})();
