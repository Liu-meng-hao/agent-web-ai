/* 轻量内联 SVG 图表：无依赖、无外链，复制到成品页即可使用。 */
(function (global) {
  "use strict";

  var NS = "http://www.w3.org/2000/svg";

  function el(name, attrs) {
    var node = document.createElementNS(NS, name);
    Object.keys(attrs || {}).forEach(function (key) {
      node.setAttribute(key, attrs[key]);
    });
    return node;
  }

  function mount(target) {
    var node = typeof target === "string" ? document.querySelector(target) : target;
    if (!node) throw new Error("找不到图表容器: " + target);
    node.innerHTML = "";
    return node;
  }

  function scale(values, height, pad) {
    var max = Math.max.apply(null, values.concat([0]));
    var min = Math.min.apply(null, values.concat([0]));
    var span = max - min || 1;
    return function (v) {
      return height - pad - ((v - min) / span) * (height - pad * 2);
    };
  }

  function bar(target, data, options) {
    var opts = options || {};
    var width = opts.width || 640;
    var height = opts.height || 260;
    var pad = 34;
    var node = mount(target);
    var svg = el("svg", { viewBox: "0 0 " + width + " " + height, role: "img", "aria-label": opts.label || "柱状图" });
    var y = scale(data.map(function (d) { return d.value; }), height, pad);
    var slot = (width - pad * 2) / Math.max(data.length, 1);
    var barWidth = Math.max(6, slot * 0.56);

    data.forEach(function (d, i) {
      var x = pad + slot * i + (slot - barWidth) / 2;
      var top = y(d.value);
      svg.appendChild(el("rect", {
        x: x, y: top, width: barWidth, height: Math.max(0, height - pad - top),
        rx: 4, fill: d.color || "var(--accent)", opacity: "0.9"
      }));
      var label = el("text", { x: pad + slot * i + slot / 2, y: height - 12, "text-anchor": "middle" });
      label.textContent = d.label;
      svg.appendChild(label);
      var value = el("text", { x: pad + slot * i + slot / 2, y: top - 6, "text-anchor": "middle" });
      value.textContent = d.display == null ? d.value : d.display;
      svg.appendChild(value);
    });
    node.appendChild(svg);
    return svg;
  }

  function line(target, data, options) {
    var opts = options || {};
    var width = opts.width || 640;
    var height = opts.height || 260;
    var pad = 34;
    var node = mount(target);
    var svg = el("svg", { viewBox: "0 0 " + width + " " + height, role: "img", "aria-label": opts.label || "折线图" });
    var y = scale(data.map(function (d) { return d.value; }), height, pad);
    var step = (width - pad * 2) / Math.max(data.length - 1, 1);
    var points = data.map(function (d, i) {
      return [pad + step * i, y(d.value)];
    });
    var path = points.map(function (p, i) {
      return (i === 0 ? "M" : "L") + p[0].toFixed(1) + " " + p[1].toFixed(1);
    }).join(" ");

    var area = path + " L" + (pad + step * (data.length - 1)) + " " + (height - pad) + " L" + pad + " " + (height - pad) + " Z";
    svg.appendChild(el("path", { d: area, fill: "var(--accent)", opacity: "0.10" }));
    svg.appendChild(el("path", { d: path, fill: "none", stroke: "var(--accent)", "stroke-width": "2.5", "stroke-linecap": "round", "stroke-linejoin": "round" }));
    points.forEach(function (p, i) {
      svg.appendChild(el("circle", { cx: p[0], cy: p[1], r: 3.5, fill: "var(--surface)", stroke: "var(--accent)", "stroke-width": 2 }));
      var label = el("text", { x: p[0], y: height - 12, "text-anchor": "middle" });
      label.textContent = data[i].label;
      svg.appendChild(label);
    });
    node.appendChild(svg);
    return svg;
  }

  function donut(target, data, options) {
    var opts = options || {};
    var size = opts.size || 240;
    var node = mount(target);
    var svg = el("svg", { viewBox: "0 0 " + size + " " + size, role: "img", "aria-label": opts.label || "环形图" });
    var total = data.reduce(function (sum, d) { return sum + Math.max(0, d.value); }, 0) || 1;
    var radius = size * 0.37;
    var stroke = size * 0.12;
    var circumference = 2 * Math.PI * radius;
    var offset = 0;

    svg.appendChild(el("circle", {
      cx: size / 2, cy: size / 2, r: radius, fill: "none",
      stroke: "var(--surface-2)", "stroke-width": stroke
    }));
    data.forEach(function (d, i) {
      var length = (Math.max(0, d.value) / total) * circumference;
      svg.appendChild(el("circle", {
        cx: size / 2, cy: size / 2, r: radius, fill: "none",
        stroke: d.color || "var(--accent)", "stroke-width": stroke,
        "stroke-dasharray": length + " " + (circumference - length),
        "stroke-dashoffset": -offset,
        transform: "rotate(-90 " + size / 2 + " " + size / 2 + ")",
        "stroke-linecap": "butt",
        opacity: String(0.95 - i * 0.08)
      }));
      offset += length;
    });
    var value = el("text", {
      x: size / 2, y: size / 2 - 2, "text-anchor": "middle",
      "font-size": "24", "font-weight": "700", fill: "var(--ink)"
    });
    value.textContent = opts.center || total;
    svg.appendChild(value);
    var caption = el("text", { x: size / 2, y: size / 2 + 20, "text-anchor": "middle" });
    caption.textContent = opts.caption || "总计";
    svg.appendChild(caption);
    node.appendChild(svg);
    return svg;
  }

  global.Charts = { bar: bar, line: line, donut: donut };
})(window);
