// Freifunk Map - Modern Frontend
// Features: Leaflet map, force-directed graph, uPlot charts, SSE live updates,
//           device pictures, URL-based filters, Grafana raw data proxy

(function () {
  'use strict';

  // ────────────────────── State ──────────────────────
  let config = {};
  let nodes = [];
  let allLinks = [];
  let nodeMap = {};
  let leafletMap, markerGroup, linkLayer;
  let markers = {};
  let selectedMarker = null;
  let selectedNodeId = null;
  let sseSource = null;
  let currentView = 'map'; // 'map' | 'graph'
  let previousTab = null; // tab before node detail was shown

  // Force graph state
  let graphSim = null;
  let graphNodes = [];
  let graphLinks = [];
  let graphCanvas, graphCtx;
  let graphTransform = { x: 0, y: 0, k: 1 };
  let graphDrag = null;
  let graphAnimFrame = null;

  // URL filter params
  let urlFilters = { domain: '', status: '', model: '', community: '' };
  let communities = []; // federation mode
  let grafanaCommunities = new Set(); // communities with Grafana stats

  // ────────────────────── Init ──────────────────────
  async function init() {
    config = await fetchJSON('/api/config');
    document.getElementById('header-brand').textContent = config.siteName || 'Freifunk';

    // Header links
    const linksEl = document.getElementById('header-links');
    (config.links || []).forEach(l => {
      const a = document.createElement('a');
      a.href = l.href;
      a.target = '_blank';
      a.textContent = l.title;
      linksEl.appendChild(a);
    });

    parseURLFilters();
    initLeafletMap();
    initTabs();
    initSearch();

    // Disable graph tab in federation mode (too many nodes for force graph)
    if (config.federation) {
      const graphTabBtn = document.querySelector('[data-tab="graph-tab"]');
      if (graphTabBtn) graphTabBtn.style.display = 'none';
    } else {
      initGraphCanvas();
    }

    await loadData();
    populateDomainFilter();
    applyURLFilters();

    if (config.federation) {
      fetchJSON('/api/communities').then(c => {
        communities = c || [];
        grafanaCommunities = new Set(
          communities.filter(c => c.grafana_url || c.dashboard_url).map(c => c.key)
        );
        // Re-populate domain filter with community names
        populateDomainFilter();
      }).catch(() => {});
    }

    connectSSE();
    handleHash();
    window.addEventListener('hashchange', handleHash);
    window.addEventListener('popstate', parseURLFilters);
  }

  // ────────────────────── URL Filters ──────────────────────
  function parseURLFilters() {
    const params = new URLSearchParams(window.location.search);
    urlFilters.domain = params.get('domain') || '';
    urlFilters.status = params.get('status') || '';
    urlFilters.model = params.get('model') || '';
    urlFilters.community = params.get('community') || '';
  }

  function applyURLFilters() {
    // Apply to list filter dropdowns
    if (urlFilters.status) {
      document.getElementById('list-filter').value = urlFilters.status;
    }
    if (urlFilters.community) {
      const comSel = document.getElementById('list-community');
      if (comSel) {
        comSel.value = urlFilters.community;
        populateDomainOptions(urlFilters.community);
      }
    }
    if (urlFilters.domain) {
      document.getElementById('list-domain').value = urlFilters.domain;
    }
    if (urlFilters.model) {
      document.getElementById('list-search').value = urlFilters.model;
    }
    renderNodeList();
    renderMarkers();

    // If filtering, switch to list tab
    if (urlFilters.domain || urlFilters.status || urlFilters.model || urlFilters.community) {
      activateTab('list-tab');
    }
  }

  function setURLFilter(key, value) {
    const params = new URLSearchParams(window.location.search);
    if (value) params.set(key, value);
    else params.delete(key);
    const qs = params.toString();
    const url = window.location.pathname + (qs ? '?' + qs : '') + window.location.hash;
    window.history.replaceState(null, '', url);
    urlFilters[key] = value || '';
  }

  function populateDomainFilter() {
    const domSel = document.getElementById('list-domain');
    const comSel = document.getElementById('list-community');
    if (!domSel) return;

    // In federation mode, build community name lookup and show community dropdown
    const communityNames = {};
    if (config.federation && communities.length > 0) {
      // Detect duplicate names to disambiguate with key
      const nameCounts = {};
      communities.forEach(c => { nameCounts[c.name] = (nameCounts[c.name] || 0) + 1; });
      communities.forEach(c => {
        if (nameCounts[c.name] > 1) {
          // Use "Name (key)" for disambiguation
          communityNames[c.key] = `${c.name} (${c.key})`;
        } else {
          communityNames[c.key] = c.name;
        }
      });

      if (comSel) {
        comSel.classList.remove('hidden');
        while (comSel.options.length > 1) comSel.remove(1);
        // Only show communities that have actual nodes
        const comKeys = new Set();
        nodes.forEach(n => {
          if (n.community) comKeys.add(n.community);
          (n.communities || []).forEach(c => comKeys.add(c));
        });

        // Build metacommunity lookup from API data
        const metaLookup = {}; // community key -> metacommunity name
        communities.forEach(c => {
          if (c.metacommunity) metaLookup[c.key] = c.metacommunity;
        });

        // Group communities by metacommunity
        const metaGroups = {}; // metacommunity name -> [keys]
        const ungrouped = [];  // keys without metacommunity
        [...comKeys].forEach(k => {
          const meta = metaLookup[k];
          if (meta) {
            if (!metaGroups[meta]) metaGroups[meta] = [];
            metaGroups[meta].push(k);
          } else {
            ungrouped.push(k);
          }
        });

        // Sort groups and entries within groups
        const sortByName = (a, b) => (communityNames[a] || a).localeCompare(communityNames[b] || b);
        ungrouped.sort(sortByName);
        Object.values(metaGroups).forEach(arr => arr.sort(sortByName));
        const sortedMetaNames = Object.keys(metaGroups).sort();

        // Add ungrouped communities first, then metacommunity optgroups
        ungrouped.forEach(k => {
          const opt = document.createElement('option');
          opt.value = k;
          opt.textContent = communityNames[k] || k;
          comSel.appendChild(opt);
        });
        sortedMetaNames.forEach(metaName => {
          const keys = metaGroups[metaName];
          if (keys.length === 1) {
            // Single community in meta — no need for a group
            const opt = document.createElement('option');
            opt.value = keys[0];
            opt.textContent = communityNames[keys[0]] || keys[0];
            comSel.appendChild(opt);
          } else {
            const group = document.createElement('optgroup');
            group.label = metaName;
            keys.forEach(k => {
              const opt = document.createElement('option');
              opt.value = k;
              opt.textContent = communityNames[k] || k;
              group.appendChild(opt);
            });
            comSel.appendChild(group);
          }
        });

        comSel.addEventListener('change', () => {
          setURLFilter('community', comSel.value);
          populateDomainOptions(comSel.value);
          renderNodeList();
          renderMarkers();
        });
      }
    }

    populateDomainOptions('');
    if (urlFilters.domain) domSel.value = urlFilters.domain;

    domSel.addEventListener('change', () => {
      setURLFilter('domain', domSel.value);
      renderNodeList();
      renderMarkers();
    });
  }

  // Populate domain dropdown, optionally filtered to a specific community
  function populateDomainOptions(communityKey) {
    const domSel = document.getElementById('list-domain');
    if (!domSel) return;
    while (domSel.options.length > 1) domSel.remove(1);
    domSel.value = '';

    // Get domains, optionally filtered by community prefix
    const domains = new Set();
    nodes.forEach(n => {
      if (!n.domain) return;
      if (communityKey) {
        if (n.community !== communityKey && !(n.communities || []).includes(communityKey)) return;
      }
      domains.add(n.domain);
    });

    const communityNames = {};
    if (config.federation && communities.length > 0) {
      communities.forEach(c => { communityNames[c.key] = c.name; });
    }

    [...domains].sort().forEach(d => {
      const opt = document.createElement('option');
      opt.value = d;
      opt.textContent = communityNames[d] || (config.domainNames && config.domainNames[d]) || d;
      domSel.appendChild(opt);
    });
  }

  // ────────────────────── Leaflet Map ──────────────────────
  function initLeafletMap() {
    leafletMap = L.map('map', {
      zoomControl: true,
      minZoom: 3,
      maxZoom: 19,
      preferCanvas: true,   // Canvas renderer — handles 50k+ markers
    }).setView(config.mapCenter || [48.135, 11.582], config.mapZoom || 10);

    const layers = {};
    (config.tileLayers || []).forEach((tl, i) => {
      const layer = L.tileLayer(tl.url, {
        attribution: tl.attribution,
        maxZoom: tl.maxZoom || 19,
      });
      layers[tl.name] = layer;
      if (i === 0) layer.addTo(leafletMap);
    });

    if (Object.keys(layers).length > 1) {
      L.control.layers(layers).addTo(leafletMap);
    }

    markerGroup = L.layerGroup().addTo(leafletMap);
    linkLayer = L.layerGroup().addTo(leafletMap);

    // Client dots canvas overlay — placed in map container (not overlay pane) so it stays fixed during pan
    clientCanvas = document.createElement('canvas');
    clientCanvas.style.cssText = 'position:absolute;top:0;left:0;pointer-events:none;z-index:450;';
    leafletMap.getContainer().appendChild(clientCanvas);
    leafletMap.on('move zoom moveend zoomend resize', drawClientDots);
  }

  // ────────────────────── Data ──────────────────────
  async function loadData() {
    const [nodeData, linkData] = await Promise.all([
      fetchJSON('/api/nodes'),
      fetchJSON('/api/links'),
    ]);
    nodes = nodeData;
    allLinks = linkData;
    nodeMap = {};
    nodes.forEach(n => {
      nodeMap[n.node_id] = n;
      if (!n.hostname) n.hostname = n.node_id; // fallback for empty names
    });
    renderMarkers();
    updateStats();
    renderNodeList();
    renderNewNodesList();
  }

  function renderNewNodesList() {
    const el = document.getElementById('new-nodes-list');
    if (!el) return;
    const sevenDays = 7 * 86400000;
    const now = Date.now();
    const newNodes = nodes
      .filter(n => n.is_online && (now - new Date(n.firstseen).getTime()) < sevenDays)
      .sort((a, b) => new Date(b.firstseen) - new Date(a.firstseen))
      .slice(0, 50);

    if (newNodes.length === 0) {
      el.innerHTML = '';
      return;
    }

    let html = `<h3 style="font-size:13px;color:var(--fg-muted);margin:12px 0 8px;padding:0 4px">🆕 New Nodes (${newNodes.length})</h3>`;
    newNodes.forEach(n => {
      const hasPos = n.lat != null && n.lng != null;
      const age = Math.floor((now - new Date(n.firstseen).getTime()) / 86400000);
      const ageStr = age === 0 ? 'today' : age + 'd ago';
      html += `<div class="node-list-item" onclick="window.FFMap.selectNode('${escAttr(n.node_id)}')" style="font-size:12px">
        <span class="node-status online" style="width:8px;height:8px;flex-shrink:0"></span>
        ${hasPos ? '<span style="font-size:10px;flex-shrink:0">🌍</span>' : '<span style="width:14px;flex-shrink:0"></span>'}
        <span class="hostname">${esc(n.hostname)}</span>
        <span class="list-meta" style="font-size:11px">${ageStr}</span>
      </div>`;
    });
    el.innerHTML = html;
  }

  function getFilteredNodes() {
    const filterVal = document.getElementById('list-filter')?.value || 'all';
    const domainVal = document.getElementById('list-domain')?.value || '';
    const communityVal = document.getElementById('list-community')?.value || '';
    const search = (document.getElementById('list-search')?.value || '').toLowerCase();

    return nodes.filter(n => {
      // Community filter
      if (communityVal) {
        if (n.community !== communityVal && !(n.communities || []).includes(communityVal)) return false;
      }
      if (domainVal && n.domain !== domainVal) return false;
      if (search && !n.hostname.toLowerCase().includes(search) && !n.node_id.includes(search) && !(n.model || '').toLowerCase().includes(search)) return false;
      switch (filterVal) {
        case 'online': return n.is_online;
        case 'offline': return !n.is_online;
        case 'new': return Date.now() - new Date(n.firstseen).getTime() < 7 * 86400000;
        case 'gateway': return n.is_gateway;
        case 'haspos': return n.lat != null && n.lng != null;
        case 'nopos': return n.lat == null || n.lng == null;
        case 'hasstats': {
          return (n.communities || [n.community]).some(c => grafanaCommunities.has(c));
        }
        default: return true;
      }
    });
  }

  const MARKER_COLORS = {
    online:          { fill: '#1566A9', stroke: '#1566A9' },
    'online-uplink': { fill: '#1566A9', stroke: '#0D4F8B' },
    'new-node':      { fill: '#93E929', stroke: '#1566A9' },
    gateway:         { fill: '#FFD600', stroke: '#1566A9' },
    offline:         { fill: '#D43E2A', stroke: '#D43E2A' },
  };

  function renderMarkers() {
    markerGroup.clearLayers();
    markers = {};
    selectedMarker = null;
    const filtered = getFilteredNodes();

    // Sort: offline first (drawn first = behind), then online on top
    filtered.sort((a, b) => (a.is_online ? 1 : 0) - (b.is_online ? 1 : 0));

    filtered.forEach(n => {
      if (n.lat == null || n.lng == null) return;
      const cls = getMarkerClass(n);
      const mc = MARKER_COLORS[cls] || MARKER_COLORS.online;
      const radius = n.is_gateway ? 7 : (n.is_online ? 6 : 3);
      const marker = L.circleMarker([n.lat, n.lng], {
        radius,
        color: mc.stroke,
        fillColor: mc.fill,
        fillOpacity: n.is_online ? 0.6 : 0.5,
        weight: 2,
        opacity: n.is_online ? 0.6 : 0.5,
        bubblingMouseEvents: false,
      });
      marker.nodeId = n.node_id;
      marker.bindTooltip(
        `<strong>${esc(n.hostname)}</strong><br>` +
        `${n.is_online ? '🟢 Online' : '🔴 Offline'}` +
        (n.clients ? ` · ${n.clients} clients` : ''),
        { direction: 'top', offset: [0, -8] }
      );
      marker.on('click', () => selectNode(n.node_id));
      markers[n.node_id] = marker;
      markerGroup.addLayer(marker);
    });
    drawClientDots();
  }

  // Draw client dots directly on a canvas overlay — fast, pixel-perfect
  let clientCanvas = null;
  const CLIENT_COLORS = { wifi24: '#FF8C00', wifi5: '#1DB954', other: '#9B59B6' };

  function drawClientDots() {
    if (!clientCanvas) return;
    const size = leafletMap.getSize();
    clientCanvas.width = size.x;
    clientCanvas.height = size.y;

    const ctx = clientCanvas.getContext('2d');
    ctx.clearRect(0, 0, size.x, size.y);

    const zoom = leafletMap.getZoom();
    if (zoom < 15) return;

    const bounds = leafletMap.getBounds();
    const radius = 3;
    const a = 1.2;
    const startDistance = 10;

    nodes.forEach(n => {
      if (!n.is_online || n.clients === 0 || n.lat == null) return;
      if (!bounds.contains([n.lat, n.lng])) return;

      const p = leafletMap.latLngToContainerPoint([n.lat, n.lng]);
      // Deterministic start angle based on node_id
      const startAngle = (parseInt((n.node_id || '00').substr(-2), 16) / 255) * 2 * Math.PI;

      const w24 = n.clients_wifi24 || 0;
      const w5 = n.clients_wifi5 || 0;
      let mode = 0;

      ctx.beginPath();
      ctx.fillStyle = CLIENT_COLORS.wifi24;

      for (let orbit = 0, i = 0; i < n.clients; orbit++) {
        const distance = startDistance + orbit * 2 * radius * a;
        const spotsInOrbit = Math.floor((Math.PI * distance) / (a * radius));
        const remaining = n.clients - i;

        for (let j = 0; j < Math.min(remaining, spotsInOrbit); i++, j++) {
          // Switch color when crossing wifi24 -> other -> wifi5 boundaries
          if (mode !== 1 && i >= w24 + w5) {
            mode = 1;
            ctx.fill();
            ctx.beginPath();
            ctx.fillStyle = CLIENT_COLORS.wifi5;
          } else if (mode === 0 && i >= w24) {
            mode = 2;
            ctx.fill();
            ctx.beginPath();
            ctx.fillStyle = CLIENT_COLORS.other;
          }
          const angle = (2 * Math.PI / spotsInOrbit) * j;
          const x = p.x + distance * Math.cos(angle + startAngle);
          const y = p.y + distance * Math.sin(angle + startAngle);

          ctx.moveTo(x, y);
          ctx.arc(x, y, radius, 0, 2 * Math.PI);
        }
      }
      ctx.fill();
    });
  }

  function getMarkerClass(n) {
    if (!n.is_online) return 'offline';
    if (n.is_gateway) return 'gateway';
    if (Date.now() - new Date(n.firstseen).getTime() < 7 * 86400000) return 'new-node';
    if (n.neighbours && n.neighbours.length > 0) return 'online-uplink';
    return 'online';
  }

  // ────────────────────── Node Selection ──────────────────────
  async function selectNode(nodeId) {
    selectedNodeId = nodeId;
    window.location.hash = '#!' + nodeId;

    // Reset previous selection
    if (selectedMarker) {
      const prev = nodeMap[selectedMarker.nodeId];
      if (prev) {
        const cls = getMarkerClass(prev);
        const mc = MARKER_COLORS[cls] || MARKER_COLORS.online;
        selectedMarker.setStyle({ color: mc.stroke, fillColor: mc.fill, weight: 2, fillOpacity: prev.is_online ? 0.6 : 0.5, opacity: prev.is_online ? 0.6 : 0.5 });
        selectedMarker.setRadius(prev.is_gateway ? 7 : (prev.is_online ? 6 : 3));
      }
    }
    // Highlight new selection
    if (markers[nodeId]) {
      const m = markers[nodeId];
      m.setStyle({ color: '#00e5ff', fillColor: '#00e5ff', weight: 4, fillOpacity: 1, opacity: 0.8 });
      m.setRadius(10);
      m.bringToFront();
      selectedMarker = m;
      leafletMap.setView(m.getLatLng(), Math.max(leafletMap.getZoom(), 15));
    }

    renderNodeLinks(nodeId);
    const detail = await fetchJSON('/api/nodes/' + nodeId);
    renderNodeDetail(detail);

    // Track which tab we came from so X can return there
    const activeTab = document.querySelector('.tab.active')?.dataset?.tab;
    if (activeTab && activeTab !== 'map-tab') {
      previousTab = activeTab;
    }
    activateTab('map-tab');
    document.getElementById('node-detail').classList.remove('hidden');
    document.getElementById('new-nodes-list')?.style.setProperty('display', 'none');
    document.getElementById('search-box')?.style.setProperty('display', 'none');
  }

  function renderNodeLinks(nodeId) {
    linkLayer.clearLayers();
    const node = nodeMap[nodeId];
    if (!node || !node.neighbours) return;
    node.neighbours.forEach(nid => {
      const nb = nodeMap[nid];
      if (!nb || nb.lat == null || node.lat == null) return;
      L.polyline([[node.lat, node.lng], [nb.lat, nb.lng]], {
        color: nb.is_online ? '#04C714' : '#F02311',
        weight: 2, opacity: 0.6,
        dashArray: nb.is_online ? null : '5,5',
      }).addTo(linkLayer);
    });
  }

  function renderNodeDetail(node) {
    const el = document.getElementById('node-detail-content');
    const devicePicUrl = getDevicePictureURL(node);

    let html = `<div class="node-header">
      <span class="node-status ${node.is_online ? 'online' : 'offline'}"></span>
      <h2>${esc(node.hostname)}</h2>
    </div>`;

    if (devicePicUrl) {
      html += `<img class="device-image" src="${devicePicUrl}" onerror="this.style.display='none'" alt="${esc(node.model || '')}">`;
    }

    // Device deprecation/EOL warnings
    if (node.model) {
      const warn = getDeviceWarning(node.model);
      if (warn) {
        html += `<div class="device-warning ${warn.level}">${warn.text}</div>`;
      }
    }

    html += `<dl class="detail-grid">`;
    html += detailRow('Status', node.is_online ? '🟢 Online' : '🔴 Offline');
    if (node.model) html += detailRow('Model', node.model);
    if (node.firmware) html += detailRow('Firmware', `${node.fw_base || ''} ${node.firmware}`);
    if (node.domain_name || node.domain) html += detailRow('Domain', node.domain_name || node.domain);
    if (node.owner) html += detailRow('Owner', node.owner);
    html += detailRow('MAC', node.mac);
    if (node.uptime && node.uptime !== '0001-01-01T00:00:00+0000') html += detailRow('Uptime', formatUptime(node.uptime));
    html += detailRow('First seen', formatDate(node.firstseen));
    html += detailRow('Last seen', formatDate(node.lastseen));
    if (node.nproc) html += detailRow('CPUs', node.nproc);
    html += detailRow('Autoupdater', node.autoupdater ? `✓ ${node.branch}` : '✗ off');
    if (node.addresses && node.addresses.length > 0) {
      html += detailRowHTML('Addresses', `<span style="font-size:11px;word-break:break-all">${node.addresses.map(a => esc(a)).join('<br>')}</span>`);
    }
    html += `</dl>`;

    if (node.is_online) {
      html += `
        <div><small style="color:var(--fg-muted)">Load (${node.load_avg.toFixed(2)})</small>${progressBar(Math.min(node.load_avg / Math.max(node.nproc, 1), 1))}</div>
        <div><small style="color:var(--fg-muted)">Memory (${(node.mem_usage * 100).toFixed(0)}%)</small>${progressBar(node.mem_usage)}</div>
        <div><small style="color:var(--fg-muted)">Storage (${(node.rootfs_usage * 100).toFixed(0)}%)</small>${progressBar(node.rootfs_usage)}</div>`;
    }

    if (node.clients > 0) {
      html += `<div class="client-card" style="margin:16px 0;padding:12px;background:var(--bg-tertiary);border-radius:var(--radius)">
        <div style="display:flex;align-items:center;gap:8px;margin-bottom:8px">
          <span style="font-size:22px">👤</span>
          <span style="font-size:24px;font-weight:bold;color:var(--fg)">${node.clients}</span>
          <span style="font-size:13px;color:var(--fg-muted)">Clients connected</span>
        </div>
        <div style="display:flex;gap:16px;font-size:13px">
          <span style="color:#58a6ff">📶 2.4 GHz: <strong>${node.clients_wifi24}</strong></span>
          <span style="color:#3fb950">📶 5 GHz: <strong>${node.clients_wifi5}</strong></span>
          ${node.clients_other > 0 ? `<span style="color:var(--fg-muted)">🔌 Other: <strong>${node.clients_other}</strong></span>` : ''}
        </div>
      </div>`;
    }

    if (node.neighbour_details && node.neighbour_details.length > 0) {
      html += `<h3 style="margin:16px 0 8px;font-size:14px">Links (${node.neighbour_details.length})</h3><ul class="neighbour-list">`;
      node.neighbour_details.forEach(nb => {
        const dist = nb.distance > 0 ? ` · ${formatDistance(nb.distance)}` : '';
        const tq = nb.tq > 0 ? ` · TQ ${(nb.tq * 100).toFixed(0)}%` : '';
        html += `<li class="neighbour-item" onclick="window.FFMap.selectNode('${escAttr(nb.node_id)}')">
          <span class="node-status ${nb.is_online ? 'online' : 'offline'}" style="width:8px;height:8px"></span>
          <span>${esc(nb.hostname || nb.node_id)}</span>
          <span style="color:var(--fg-muted);font-size:12px;margin-left:auto">${nb.link_type || ''}${tq}${dist}</span>
        </li>`;
      });
      html += `</ul>`;
    }

    // Grafana charts via our proxy (works in both single and federation mode)
    const grafanaLink = getGrafanaDashboardLink(node.node_id);

    // Charts placeholder — loadGrafanaCharts will fill or hide
    if (node.is_online) {
      html += `<div class="charts-container hidden" id="charts-container">
        <div class="chart-duration-tabs" style="display:flex;gap:8px;margin-bottom:12px">
          <button class="chart-tab active" data-duration="24h" style="padding:4px 10px;font-size:12px;border:1px solid var(--border);background:var(--accent);color:#fff;border-radius:var(--radius);cursor:pointer">24h</button>
          <button class="chart-tab" data-duration="7d" style="padding:4px 10px;font-size:12px;border:1px solid var(--border);background:var(--bg-tertiary);color:var(--fg-muted);border-radius:var(--radius);cursor:pointer">7d</button>
        </div>
        <div class="chart-wrapper" id="chart-clients" style="min-height:200px"></div>
        <div class="chart-wrapper" id="chart-traffic" style="min-height:200px"></div>
      </div>`;
    }
    if (grafanaLink) {
      html += `<a href="${grafanaLink}" target="_blank" class="grafana-link" id="grafana-link">📊 View in Grafana</a>`;
    }

    el.innerHTML = html;

    if (node.is_online) {
      loadGrafanaCharts(node.node_id, grafanaLink, '24h');
      // Wire up duration tabs
      el.querySelectorAll('.chart-tab').forEach(btn => {
        btn.addEventListener('click', () => {
          el.querySelectorAll('.chart-tab').forEach(b => {
            b.classList.remove('active');
            b.style.background = 'var(--bg-tertiary)';
            b.style.color = 'var(--fg-muted)';
          });
          btn.classList.add('active');
          btn.style.background = 'var(--accent)';
          btn.style.color = '#fff';
          loadGrafanaCharts(node.node_id, grafanaLink, btn.dataset.duration);
        });
      });
    }
  }

  // Get Grafana dashboard link for a node using community dashboard templates
  function getGrafanaDashboardLink(nodeId) {
    if (!config.federation || !communities.length) return null;
    const node = nodeMap[nodeId];
    if (!node) return null;
    // Try each community the node belongs to
    const comms = node.communities || (node.community ? [node.community] : []);
    // For gateway nodes, strip the _community suffix to get the original MAC for Grafana
    let grafanaNodeId = nodeId;
    if (node.is_gateway) {
      for (const ck of comms) {
        const suffix = '_' + ck;
        if (nodeId.endsWith(suffix)) {
          grafanaNodeId = nodeId.slice(0, -suffix.length);
          break;
        }
      }
    }
    for (const ck of comms) {
      const community = communities.find(c => c.key === ck);
      if (!community) continue;
      if (community.dashboard_url) {
        return community.dashboard_url.replace(/\{NODE_ID\}/g, grafanaNodeId);
      }
      if (community.grafana_url) {
        return community.grafana_url;
      }
    }
    return null;
  }

  // ────────────────────── Device Pictures ──────────────────────
  // ────────────────────── Device Deprecation Warnings ──────────────────────
  // EOL devices no longer receive firmware/security updates
  const EOL_DEVICES = new Set([
    'A5-V11','AP121','AP121U','D-Link DIR-615','AVM FRITZ!Box 7320','AVM FRITZ!Box 7330',
    'TP-Link TL-MR3020 v1','TP-Link TL-MR3040 v1','TP-Link TL-MR3040 v2',
    'TP-Link TL-WR740N/ND v1','TP-Link TL-WR740N/ND v3','TP-Link TL-WR740N/ND v4','TP-Link TL-WR740N/ND v5',
    'TP-Link TL-WR741N/ND v1','TP-Link TL-WR741N/ND v3','TP-Link TL-WR741N/ND v4','TP-Link TL-WR741N/ND v5',
    'TP-Link TL-WR841N/ND v3','TP-Link TL-WR841N/ND v5','TP-Link TL-WR841N/ND v7',
    'TP-Link TL-WR841N/ND v8','TP-Link TL-WR841N/ND v9','TP-Link TL-WR841N/ND v10',
    'TP-Link TL-WR841N/ND v11','TP-Link TL-WR841N/ND v12',
    'TP-Link TL-WR842N/ND v1','TP-Link TL-WR842N/ND v2',
    'TP-Link TL-WR940N v1','TP-Link TL-WR940N v2','TP-Link TL-WR940N v3',
    'TP-Link TL-WR940N v4','TP-Link TL-WR940N v5','TP-Link TL-WR940N v6',
    'TP-Link TL-WR941N/ND v2','TP-Link TL-WR941N/ND v3','TP-Link TL-WR941N/ND v4',
    'TP-Link TL-WR941N/ND v5','TP-Link TL-WR941N/ND v6',
    'TP-Link TL-WR1043N/ND v1','TP-Link TL-WA901N/ND v1','TP-Link TL-WA901N/ND v2',
    'TP-Link TL-WA901N/ND v3','TP-Link TL-WA901N/ND v4','TP-Link TL-WA901N/ND v5',
    'TP-Link TL-WR703N v1','TP-Link TL-WR710N v1','TP-Link TL-WR710N v2',
    'TP-Link TL-MR13U v1','TP-Link TL-MR3220 v1','TP-Link TL-MR3220 v2',
    'TP-Link TL-MR3420 v1','TP-Link TL-MR3420 v2',
    'TP-Link RE200 v1','TP-Link RE200 v2','TP-Link RE200 v3','TP-Link RE200 v4',
    'TP-Link RE305 v1','TP-Link RE305 v2','TP-Link RE305 v3',
    'TP-Link RE450 v1','TP-Link RE450 v2','TP-Link RE450 v3',
    'Ubiquiti NanoStation loco M2','Ubiquiti NanoStation M2','Ubiquiti PicoStation M2',
    'Ubiquiti Bullet M','Ubiquiti Bullet M2','Ubiquiti AirRouter',
    'VoCore 8M','VoCore 16M',
  ]);
  // Deprecated devices will lose support soon
  const DEPRECATED_DEVICES = new Set([
    'TP-Link Archer C2 v3','TP-Link Archer C6 v2','TP-Link Archer C6 v3',
    'TP-Link Archer C20 v4','TP-Link Archer C25 v1',
    'TP-Link Archer C50 v1','TP-Link Archer C50 v3','TP-Link Archer C50 v4','TP-Link Archer C50 v6',
    'TP-Link Archer C60 v1','TP-Link Archer C60 v2',
    'TP-Link CPE210 v1.0','TP-Link CPE210 v1.1','TP-Link CPE210 v2.0','TP-Link CPE210 v3.0',
    'TP-Link CPE510 v1.0','TP-Link CPE510 v1.1','TP-Link CPE510 v2','TP-Link CPE510 v3',
    'TP-Link TL-WR902AC v1','TP-Link TL-WR902AC v3',
    'Ubiquiti EdgeRouter X','Ubiquiti EdgeRouter X SFP',
    'Xiaomi Redmi Router AX6S','D-Link DGS-1210',
  ]);

  function getDeviceWarning(model) {
    if (!model) return null;
    // Check exact match first, then prefix match
    if (EOL_DEVICES.has(model)) {
      const eolLink = config.eolInfoURL ? ` <a href="${esc(config.eolInfoURL)}" target="_blank">More info</a>` : '';
      return { level: 'eol', text: `⛔ This device is end-of-life and no longer receives security updates.${eolLink}` };
    }
    if (DEPRECATED_DEVICES.has(model)) {
      return { level: 'deprecated', text: '⚠️ This device is deprecated and will lose firmware support soon. Consider replacing it.' };
    }
    // Fuzzy match for slight model name variations
    for (const d of EOL_DEVICES) {
      if (model.startsWith(d) || d.startsWith(model)) {
        return { level: 'eol', text: '⛔ This device is end-of-life and no longer receives security updates.' };
      }
    }
    return null;
  }

  function getDevicePictureURL(node) {
    if (!config.devicePictureURL) return null;
    if (node.image_name) {
      return config.devicePictureURL.replace('{MODEL}', node.image_name.replace(/[^a-z0-9\-]/gi, '_'));
    }
    if (node.model) {
      return config.devicePictureURL.replace('{MODEL}',
        node.model.replace(/[^a-z0-9\-]/gi, '_').replace(/_+/g, '_').replace(/^_|_$/g, '').toLowerCase());
    }
    return null;
  }

  // ────────────────────── Grafana Charts via raw data + uPlot ──────────────────────
  async function loadGrafanaCharts(nodeId, grafanaLink, duration) {
    if (typeof uPlot === 'undefined') return;
    duration = duration || '24h';
    const chartsContainer = document.getElementById('charts-container');
    let anyData = false;

    const charts = [
      { metric: 'clients', target: 'chart-clients', title: 'Clients',
        series: [{ label: 'Clients', stroke: '#58a6ff', fill: 'rgba(88,166,255,0.1)' }] },
      { metric: 'traffic', target: 'chart-traffic', title: 'Traffic (bps)',
        series: [
          { label: 'Forward', stroke: '#3fb950', fill: 'rgba(63,185,80,0.08)' },
          { label: 'RX', stroke: '#58a6ff', fill: 'rgba(88,166,255,0.08)' },
          { label: 'TX', stroke: '#f0883e', fill: 'rgba(240,136,62,0.08)' },
        ] },
    ];

    for (const chart of charts) {
      const container = document.getElementById(chart.target);
      if (!container) continue;
      container.innerHTML = `<h4 style="margin:0 0 4px;font-size:13px;color:var(--fg-muted)">${chart.title}</h4><div style="color:var(--fg-muted);font-size:12px">Loading...</div>`;

      try {
        const data = await fetchJSON(`/api/metrics/${nodeId}?metric=${chart.metric}&duration=${duration}`);
        if (!data || !data.length || !data[0].times || !data[0].times.length) {
          container.innerHTML = `<h4 style="margin:0 0 4px;font-size:13px;color:var(--fg-muted)">${chart.title}</h4><span style="color:var(--fg-muted);font-size:12px">No data</span>`;
          continue;
        }

        // Build uPlot data: [timestamps, ...series]
        const timestamps = data[0].times;
        const uData = [timestamps];
        data.forEach(d => uData.push(d.values));

        const isTraffic = chart.metric === 'traffic';
        const valueFmt = isTraffic ? fmtBps : (v) => v == null ? '—' : String(Math.round(v));

        const seriesOpts = [{ label: 'Time' }];
        chart.series.forEach((s, i) => {
          seriesOpts.push({
            label: data[i]?.name ? friendlyName(data[i].name) : s.label,
            stroke: s.stroke,
            fill: s.fill,
            width: 1.5,
            value: (u, v) => v == null ? '—' : valueFmt(v),
          });
        });

        container.innerHTML = `<h4 style="margin:0 0 4px;font-size:13px;color:var(--fg-muted)">${chart.title}</h4>`;
        const plotEl = document.createElement('div');
        container.appendChild(plotEl);

        const width = container.offsetWidth || 320;
        new uPlot({
          width: width,
          height: 140,
          cursor: { show: true, drag: { x: false, y: false } },
          legend: { show: true, live: true },
          scales: { x: { time: true }, y: { auto: true } },
          axes: [
            { stroke: '#8b949e', grid: { stroke: '#21262d' }, ticks: { stroke: '#30363d' },
              font: '10px sans-serif', labelFont: '10px sans-serif' },
            { stroke: '#8b949e', grid: { stroke: '#21262d' }, ticks: { stroke: '#30363d' },
              font: '10px sans-serif', labelFont: '10px sans-serif', size: isTraffic ? 70 : 50,
              values: (u, vals) => vals.map(valueFmt) },
          ],
          series: seriesOpts,
        }, uData, plotEl);
        anyData = true;

      } catch (e) {
        container.innerHTML = `<h4 style="margin:0 0 4px;font-size:13px;color:var(--fg-muted)">${chart.title}</h4><span style="color:var(--fg-muted);font-size:12px">Chart unavailable</span>`;
      }
    }

    // If we got any chart data, show the container; otherwise hide it
    if (chartsContainer) {
      if (anyData) {
        chartsContainer.classList.remove('hidden');
      } else {
        chartsContainer.remove();
      }
    }
  }

  function friendlyName(name) {
    return name.replace('traffic_', '').replace('_', ' ');
  }

  function fmtBps(v) {
    if (v == null) return '';
    const abs = Math.abs(v);
    if (abs >= 1e9) return (v / 1e9).toFixed(1) + ' Gbps';
    if (abs >= 1e6) return (v / 1e6).toFixed(1) + ' Mbps';
    if (abs >= 1e3) return (v / 1e3).toFixed(1) + ' kbps';
    return Math.round(v) + ' bps';
  }

  // ────────────────────── Force-Directed Graph ──────────────────────
  function initGraphCanvas() {
    graphCanvas = document.getElementById('graph-canvas');
    graphCtx = graphCanvas.getContext('2d');

    // Mouse / touch events
    graphCanvas.addEventListener('wheel', onGraphWheel, { passive: false });
    graphCanvas.addEventListener('mousedown', onGraphMouseDown);
    graphCanvas.addEventListener('mousemove', onGraphMouseMove);
    graphCanvas.addEventListener('mouseup', onGraphMouseUp);
    graphCanvas.addEventListener('click', onGraphClick);

    // Controls
    document.getElementById('graph-only-online')?.addEventListener('change', buildGraph);
    document.getElementById('graph-hide-vpn')?.addEventListener('change', buildGraph);
  }

  function showGraph() {
    currentView = 'graph';
    document.getElementById('map').style.display = 'none';
    graphCanvas.classList.remove('hidden');
    resizeGraph();
    buildGraph();
    window.addEventListener('resize', resizeGraph);
  }

  function hideGraph() {
    currentView = 'map';
    graphCanvas.classList.add('hidden');
    document.getElementById('map').style.display = '';
    leafletMap.invalidateSize();
    cancelAnimationFrame(graphAnimFrame);
    graphSim = null;
    window.removeEventListener('resize', resizeGraph);
  }

  function resizeGraph() {
    const rect = graphCanvas.parentElement.getBoundingClientRect();
    const sidebar = document.getElementById('sidebar');
    const header = document.getElementById('header');
    const sw = window.innerWidth <= 768 ? 0 : sidebar.offsetWidth;
    graphCanvas.width = (window.innerWidth - sw) * devicePixelRatio;
    graphCanvas.height = (window.innerHeight - header.offsetHeight) * devicePixelRatio;
    graphCanvas.style.width = (window.innerWidth - sw) + 'px';
    graphCanvas.style.height = (window.innerHeight - header.offsetHeight) + 'px';
    graphCtx.scale(devicePixelRatio, devicePixelRatio);
  }

  function buildGraph() {
    const onlyOnline = document.getElementById('graph-only-online')?.checked ?? true;
    const hideVpn = document.getElementById('graph-hide-vpn')?.checked ?? true;

    // Build node set
    const nodeSet = new Set();
    let filteredLinks = allLinks.filter(l => {
      if (hideVpn && l.type && l.type.startsWith('vpn')) return false;
      const sn = nodeMap[l.source], tn = nodeMap[l.target];
      if (!sn || !tn) return false;
      if (onlyOnline && (!sn.is_online || !tn.is_online)) return false;
      return true;
    });

    filteredLinks.forEach(l => { nodeSet.add(l.source); nodeSet.add(l.target); });

    // Also add isolated online nodes (no mesh links)
    if (!onlyOnline) {
      nodes.forEach(n => nodeSet.add(n.node_id));
    } else {
      nodes.forEach(n => { if (n.is_online) nodeSet.add(n.node_id); });
    }

    const nodeIndex = {};
    graphNodes = [];
    nodeSet.forEach(id => {
      const n = nodeMap[id];
      if (!n) return;
      nodeIndex[id] = graphNodes.length;
      graphNodes.push({
        id, x: (Math.random() - 0.5) * 600,
        y: (Math.random() - 0.5) * 600,
        vx: 0, vy: 0,
        node: n,
      });
    });

    graphLinks = filteredLinks.map(l => ({
      source: nodeIndex[l.source],
      target: nodeIndex[l.target],
      tq: (l.source_tq + l.target_tq) / 2,
      type: l.type,
      link: l,
    })).filter(l => l.source !== undefined && l.target !== undefined);

    // Reset view
    graphTransform = { x: graphCanvas.width / devicePixelRatio / 2, y: graphCanvas.height / devicePixelRatio / 2, k: 0.8 };

    startSimulation();
  }

  function startSimulation() {
    // Simple velocity Verlet force simulation
    const alpha = { value: 1.0, decay: 0.02, min: 0.001 };

    function tick() {
      if (alpha.value < alpha.min) {
        drawGraph();
        return;
      }
      alpha.value *= (1 - alpha.decay);

      // Repulsion (charge)
      for (let i = 0; i < graphNodes.length; i++) {
        for (let j = i + 1; j < graphNodes.length; j++) {
          const a = graphNodes[i], b = graphNodes[j];
          let dx = b.x - a.x, dy = b.y - a.y;
          let d2 = dx * dx + dy * dy;
          if (d2 < 1) d2 = 1;
          const strength = -120 * alpha.value / d2;
          const fx = dx * strength, fy = dy * strength;
          a.vx -= fx; a.vy -= fy;
          b.vx += fx; b.vy += fy;
        }
      }

      // Link attraction
      graphLinks.forEach(l => {
        const a = graphNodes[l.source], b = graphNodes[l.target];
        const dx = b.x - a.x, dy = b.y - a.y;
        const d = Math.sqrt(dx * dx + dy * dy) || 1;
        const target = 40;
        const f = (d - target) * 0.05 * alpha.value;
        const fx = dx / d * f, fy = dy / d * f;
        a.vx += fx; a.vy += fy;
        b.vx -= fx; b.vy -= fy;
      });

      // Center gravity
      graphNodes.forEach(n => {
        n.vx -= n.x * 0.01 * alpha.value;
        n.vy -= n.y * 0.01 * alpha.value;
      });

      // Velocity decay + position update
      graphNodes.forEach(n => {
        if (n.fixed) return;
        n.vx *= 0.6;
        n.vy *= 0.6;
        n.x += n.vx;
        n.y += n.vy;
      });

      drawGraph();
      graphAnimFrame = requestAnimationFrame(tick);
    }

    cancelAnimationFrame(graphAnimFrame);
    tick();
  }

  function drawGraph() {
    const w = graphCanvas.width / devicePixelRatio;
    const h = graphCanvas.height / devicePixelRatio;
    const ctx = graphCtx;
    ctx.save();
    ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
    ctx.clearRect(0, 0, w, h);
    ctx.translate(graphTransform.x, graphTransform.y);
    ctx.scale(graphTransform.k, graphTransform.k);

    // Draw links
    ctx.lineWidth = 1;
    graphLinks.forEach(l => {
      const a = graphNodes[l.source], b = graphNodes[l.target];
      ctx.beginPath();
      ctx.moveTo(a.x, a.y);
      ctx.lineTo(b.x, b.y);
      const tq = l.tq || 0;
      ctx.strokeStyle = tqColor(tq);
      ctx.globalAlpha = l.type?.startsWith('vpn') ? 0.15 : 0.5;
      ctx.stroke();
    });
    ctx.globalAlpha = 1;

    // Draw nodes
    const nodeRadius = 5;
    graphNodes.forEach(gn => {
      const n = gn.node;
      ctx.beginPath();
      ctx.arc(gn.x, gn.y, n.is_gateway ? 8 : nodeRadius, 0, Math.PI * 2);
      ctx.fillStyle = n.is_online
        ? (n.is_gateway ? '#d2a8ff' : '#1566A9')
        : '#f85149';
      ctx.fill();

      // Highlight selected
      if (n.node_id === selectedNodeId) {
        ctx.beginPath();
        ctx.arc(gn.x, gn.y, 12, 0, Math.PI * 2);
        ctx.strokeStyle = '#58a6ff';
        ctx.lineWidth = 2;
        ctx.stroke();
      }

      // Label at higher zoom
      if (graphTransform.k > 1.2) {
        ctx.fillStyle = '#e6edf3';
        ctx.font = '9px sans-serif';
        ctx.textAlign = 'center';
        ctx.fillText(n.hostname, gn.x, gn.y + 14);
      }
    });

    ctx.restore();
  }

  function tqColor(tq) {
    if (tq <= 0) return '#8b949e';
    const r = Math.round(240 - tq * 236);
    const g = Math.round(35 + tq * 164);
    return `rgb(${r},${g},20)`;
  }

  // Graph interaction
  function graphScreenToWorld(sx, sy) {
    return {
      x: (sx - graphTransform.x) / graphTransform.k,
      y: (sy - graphTransform.y) / graphTransform.k,
    };
  }

  function findNodeAt(sx, sy) {
    const { x, y } = graphScreenToWorld(sx, sy);
    const r = 12 / graphTransform.k;
    for (let i = graphNodes.length - 1; i >= 0; i--) {
      const n = graphNodes[i];
      if (Math.abs(n.x - x) < r && Math.abs(n.y - y) < r) return n;
    }
    return null;
  }

  function onGraphWheel(e) {
    e.preventDefault();
    const scale = e.deltaY > 0 ? 0.9 : 1.1;
    const rect = graphCanvas.getBoundingClientRect();
    const mx = e.clientX - rect.left, my = e.clientY - rect.top;
    graphTransform.x = mx - (mx - graphTransform.x) * scale;
    graphTransform.y = my - (my - graphTransform.y) * scale;
    graphTransform.k *= scale;
    drawGraph();
  }

  function onGraphMouseDown(e) {
    const rect = graphCanvas.getBoundingClientRect();
    const sx = e.clientX - rect.left, sy = e.clientY - rect.top;
    const hit = findNodeAt(sx, sy);
    if (hit) {
      graphDrag = { node: hit, startX: sx, startY: sy, mode: 'node' };
      hit.fixed = true;
    } else {
      graphDrag = { startX: sx, startY: sy, oX: graphTransform.x, oY: graphTransform.y, mode: 'pan' };
    }
  }

  function onGraphMouseMove(e) {
    if (!graphDrag) return;
    const rect = graphCanvas.getBoundingClientRect();
    const sx = e.clientX - rect.left, sy = e.clientY - rect.top;

    if (graphDrag.mode === 'node') {
      const { x, y } = graphScreenToWorld(sx, sy);
      graphDrag.node.x = x;
      graphDrag.node.y = y;
      graphDrag.node.vx = 0;
      graphDrag.node.vy = 0;
    } else {
      graphTransform.x = graphDrag.oX + (sx - graphDrag.startX);
      graphTransform.y = graphDrag.oY + (sy - graphDrag.startY);
    }
    drawGraph();
  }

  function onGraphMouseUp() {
    if (graphDrag && graphDrag.mode === 'node') {
      graphDrag.node.fixed = false;
    }
    graphDrag = null;
  }

  function onGraphClick(e) {
    const rect = graphCanvas.getBoundingClientRect();
    const hit = findNodeAt(e.clientX - rect.left, e.clientY - rect.top);
    if (hit) selectNode(hit.id);
  }

  // ────────────────────── SSE ──────────────────────
  function connectSSE() {
    const dot = document.querySelector('.sse-dot');
    sseSource = new EventSource('/api/events');

    sseSource.onopen = () => { dot.className = 'sse-dot connected'; };
    sseSource.onerror = () => { dot.className = 'sse-dot error'; };

    sseSource.onmessage = (e) => {
      try {
        const update = JSON.parse(e.data);
        applySSEUpdate(update);
      } catch (err) { console.error('SSE parse error:', err); }
    };
  }

  function applySSEUpdate(update) {
    if (update.stats) {
      renderStatsFromData(update.stats);
      updateHeaderStats(update.stats);
    }
    if (update.type === 'full') { loadData(); return; }

    // If there are new or removed nodes, do a full reload since we don't
    // have the full node data in the diff — only IDs
    if ((update.new && update.new.length) || (update.gone && update.gone.length)) {
      loadData();
      return;
    }

    let needsMarkerUpdate = false;
    if (update.changed) {
      update.changed.forEach(diff => {
        const n = nodeMap[diff.node_id];
        if (!n) return;
        const wasOnline = n.is_online;
        n.is_online = diff.is_online;
        n.clients = diff.clients;
        n.load_avg = diff.load_avg;
        n.mem_usage = diff.mem_usage;
        if (wasOnline !== diff.is_online) needsMarkerUpdate = true;
        if (diff.node_id === selectedNodeId) {
          const st = document.querySelector('.node-status');
          if (st) st.className = `node-status ${diff.is_online ? 'online' : 'offline'}`;
        }
      });
    }
    if (needsMarkerUpdate) renderMarkers();
  }

  // ────────────────────── Stats ──────────────────────
  function updateStats() {
    const online = nodes.filter(n => n.is_online);
    updateHeaderStats({
      total_nodes: nodes.length, online_nodes: online.length,
      total_clients: online.reduce((s, n) => s + n.clients, 0),
      gateways: online.filter(n => n.is_gateway).length,
    });
  }

  function updateHeaderStats(s) {
    document.getElementById('header-stats').textContent =
      `${s.online_nodes}/${s.total_nodes} nodes · ${s.total_clients} clients · ${s.gateways} gw`;
  }

  function renderStatsFromData(stats) {
    const el = document.getElementById('stats-content');
    let html = '';

    // Federation banner
    if (config.federation && communities.length > 0) {
      const activeCommunities = communities.filter(c => c.active).length;
      html += `<div class="stat-card" style="border-left:3px solid var(--accent)">
        <h3>Federation Mode</h3>
        <p style="font-size:12px;color:var(--fg-muted)">Auto-discovering communities from <a href="https://api.freifunk.net/" target="_blank">api.freifunk.net</a></p>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:8px">
          <div><div class="stat-number">${communities.length}</div><small style="color:var(--fg-muted)">Discovered</small></div>
          <div><div class="stat-number">${activeCommunities}</div><small style="color:var(--fg-muted)">Active Sources</small></div>
        </div>
      </div>`;
    }

    html += `<div class="stat-card"><h3>Overview</h3>
      <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px">
        <div><div class="stat-number">${stats.online_nodes}</div><small style="color:var(--fg-muted)">Online</small></div>
        <div><div class="stat-number">${stats.total_clients}</div><small style="color:var(--fg-muted)">Clients</small></div>
        <div><div class="stat-number">${stats.total_nodes}</div><small style="color:var(--fg-muted)">Total Nodes</small></div>
        <div><div class="stat-number">${stats.online_nodes > 0 ? (stats.total_clients / stats.online_nodes).toFixed(2) : '0'}</div><small style="color:var(--fg-muted)">Clients/Node</small></div>
      </div></div>`;

    // Show Communities (from community tagging) and Domains separately in federation mode
    const sections = [];
    if (config.federation && stats.communities) {
      // Group communities by metacommunity to avoid duplicates
      const metaLookup = {};
      communities.forEach(c => { if (c.metacommunity) metaLookup[c.key] = c.metacommunity; });
      const communityNames = {};
      communities.forEach(c => { communityNames[c.key] = c.name; });
      // API-reported node count (from freifunk directory, not from our data)
      const apiNodes = {};
      communities.forEach(c => { apiNodes[c.key] = c.nodes || 0; });

      const metaTotals = {};   // metacommunity name -> total nodes (deduplicated)
      const metaKeys = {};     // metacommunity name -> [keys]
      const ungrouped = [];    // [{key, name, count}] — no metacommunity

      for (const [key, count] of Object.entries(stats.communities)) {
        const meta = metaLookup[key];
        if (meta) {
          if (!metaKeys[meta]) { metaKeys[meta] = []; metaTotals[meta] = 0; }
          metaKeys[meta].push(key);
          if (count > metaTotals[meta]) metaTotals[meta] = count;
        } else {
          ungrouped.push({ key, name: communityNames[key] || key, count });
        }
      }

      // Compute per-community client stats from node data
      const commStats = {}; // name -> {nodes, online, clients}
      nodes.forEach(n => {
        const comm = n.community;
        if (!comm) return;
        // Resolve display name via meta grouping or community name
        const meta = metaLookup[comm];
        const displayName = meta || communityNames[comm] || comm;
        if (!commStats[displayName]) commStats[displayName] = { nodes: 0, online: 0, clients: 0 };
        commStats[displayName].nodes++;
        if (n.is_online) {
          commStats[displayName].online++;
          commStats[displayName].clients += n.clients || 0;
        }
      });

      // Deduplicate ungrouped communities that share a data source:
      // they'll have the exact same node count. Keep the one with the
      // highest API-reported node count (= the "real" owner of that source).
      const byCount = {};
      for (const c of ungrouped) {
        if (!byCount[c.count]) byCount[c.count] = [];
        byCount[c.count].push(c);
      }
      const standalone = {};
      for (const group of Object.values(byCount)) {
        if (group.length === 1) {
          standalone[group[0].name] = group[0].count;
        } else {
          group.sort((a, b) => apiNodes[b.key] - apiNodes[a.key]);
          standalone[group[0].name] = group[0].count;
        }
      }

      // Merge into one sorted list
      const merged = {};
      for (const [meta, count] of Object.entries(metaTotals)) merged[meta] = count;
      for (const [name, count] of Object.entries(standalone)) merged[name] = count;

      // Build community section with clients/node ratio
      const commSorted = Object.entries(merged).sort((a, b) => b[1] - a[1]);
      html += `<div class="stat-card"><h3>Communities (${commSorted.length})</h3>`;
      commSorted.slice(0, 30).forEach(([name]) => {
        const cs = commStats[name] || { nodes: 0, online: 0, clients: 0 };
        const ratio = cs.online > 0 ? (cs.clients / cs.online).toFixed(1) : '0';
        html += `<div style="padding:4px 0;border-bottom:1px solid var(--border)">
          <div style="font-size:13px">${esc(name)}</div>
          <div style="font-size:11px;color:var(--fg-muted)">${cs.online}/${cs.nodes} nodes · ${cs.clients} clients · <strong style="color:var(--fg)">${ratio}</strong> c/n</div>
        </div>`;
      });
      html += `</div>`;
    }
    sections.push(['Domains', stats.domains, 20]);
    sections.push(['Gluon Version', stats.gluon_versions, 15]);
    sections.push(['Firmware', stats.firmwares, 15]);
    sections.push(['Models', stats.models, 15]);

    sections
      .forEach(([title, data, limit]) => {
        if (!data) return;
        const sorted = Object.entries(data).sort((a, b) => b[1] - a[1]);
        html += `<div class="stat-card"><h3>${title} (${sorted.length})</h3>`;
        sorted.slice(0, limit).forEach(([k, v]) => {
          html += `<div class="stat-row"><span class="label">${esc(k)}</span><span class="value">${v}</span></div>`;
        });
        html += `</div>`;
      });
    el.innerHTML = html;
  }

  // ────────────────────── Node List ──────────────────────
  function getUptimeMs(n) {
    if (!n.uptime || n.uptime === '0001-01-01T00:00:00+0000') return 0;
    try { return Math.max(0, Date.now() - new Date(n.uptime).getTime()); } catch { return 0; }
  }

  function renderNodeList() {
    const el = document.getElementById('node-list');
    const sortVal = document.getElementById('list-sort')?.value || 'name';

    let filtered = getFilteredNodes();

    // Sort
    switch (sortVal) {
      case 'clients':
        filtered.sort((a, b) => b.clients - a.clients);
        break;
      case 'uptime':
        filtered.sort((a, b) => getUptimeMs(b) - getUptimeMs(a));
        break;
      case 'links':
        filtered.sort((a, b) => (b.neighbours?.length || 0) - (a.neighbours?.length || 0));
        break;
      case 'firstseen':
        filtered.sort((a, b) => new Date(b.firstseen) - new Date(a.firstseen));
        break;
      default: // name: online first, then alpha
        filtered.sort((a, b) => {
          if (a.is_online !== b.is_online) return a.is_online ? -1 : 1;
          return a.hostname.localeCompare(b.hostname, undefined, { sensitivity: 'base' });
        });
    }

    const limit = 500, total = filtered.length;
    filtered = filtered.slice(0, limit);
    let html = total > limit ? `<div style="padding:8px;color:var(--fg-muted);font-size:12px">Showing ${limit} of ${total}</div>` : '';
    html += `<div style="padding:4px 10px;font-size:11px;color:var(--fg-muted)">${total} nodes</div>`;

    filtered.forEach(n => {
      const linkCount = n.neighbours?.length || 0;
      const uptimeStr = (n.is_online && n.uptime && n.uptime !== '0001-01-01T00:00:00+0000') ? formatUptime(n.uptime) : '';
      const hasPos = (n.lat != null && n.lng != null);
      html += `<div class="node-list-item" onclick="window.FFMap.selectNode('${escAttr(n.node_id)}')">
        <span class="node-status ${n.is_online ? 'online' : 'offline'}" style="width:8px;height:8px;flex-shrink:0"></span>
        ${hasPos ? '<span title="Has location" style="font-size:10px;flex-shrink:0">🌍</span>' : '<span style="width:14px;flex-shrink:0"></span>'}
        <span class="hostname">${esc(n.hostname)}</span>
        <span class="list-meta">${n.clients > 0 ? n.clients + '👤 ' : ''}${linkCount > 0 ? linkCount + '🔗 ' : ''}${uptimeStr}</span>
      </div>`;
    });
    el.innerHTML = html;
  }

  // ────────────────────── Search ──────────────────────
  function initSearch() {
    const input = document.getElementById('search-input');
    const results = document.getElementById('search-results');
    input.addEventListener('input', () => {
      const q = input.value.toLowerCase().trim();
      if (q.length < 2) { results.classList.add('hidden'); return; }
      const matches = nodes.filter(n =>
        n.hostname.toLowerCase().includes(q) || n.node_id.includes(q) || (n.mac && n.mac.includes(q))
      ).slice(0, 10);
      if (!matches.length) { results.classList.add('hidden'); return; }
      results.innerHTML = matches.map(n =>
        `<div class="search-item" onclick="window.FFMap.selectNode('${escAttr(n.node_id)}');document.getElementById('search-results').classList.add('hidden')">
          <span class="node-status ${n.is_online ? 'online' : 'offline'}" style="width:8px;height:8px"></span>
          ${esc(n.hostname)}
        </div>`
      ).join('');
      results.classList.remove('hidden');
    });
    input.addEventListener('blur', () => setTimeout(() => results.classList.add('hidden'), 200));

    document.getElementById('list-search').addEventListener('input', () => {
      setURLFilter('model', document.getElementById('list-search').value);
      renderNodeList();
      renderMarkers();
    });
    document.getElementById('list-filter').addEventListener('change', () => {
      setURLFilter('status', document.getElementById('list-filter').value === 'all' ? '' : document.getElementById('list-filter').value);
      renderNodeList();
      renderMarkers();
    });
    document.getElementById('list-sort')?.addEventListener('change', () => renderNodeList());
  }

  // ────────────────────── Tabs ──────────────────────
  function initTabs() {
    document.querySelectorAll('.tab').forEach(t => {
      t.addEventListener('click', () => activateTab(t.dataset.tab));
    });
    document.getElementById('node-detail-close').addEventListener('click', () => {
      document.getElementById('node-detail').classList.add('hidden');
      document.getElementById('new-nodes-list')?.style.removeProperty('display');
      document.getElementById('search-box')?.style.removeProperty('display');
      selectedNodeId = null;
      window.location.hash = '';
      linkLayer.clearLayers();
      if (selectedMarker) {
        const prev = nodeMap[selectedMarker.nodeId];
        if (prev) {
          const cls = getMarkerClass(prev);
          const mc = MARKER_COLORS[cls] || MARKER_COLORS.online;
          selectedMarker.setStyle({ color: mc.stroke, fillColor: mc.fill, weight: 2, fillOpacity: prev.is_online ? 0.6 : 0.5, opacity: prev.is_online ? 0.6 : 0.5 });
          selectedMarker.setRadius(prev.is_gateway ? 7 : (prev.is_online ? 6 : 3));
        }
        selectedMarker = null;
      }
      // Return to previous tab (e.g. Nodes list) if we came from there
      if (previousTab && previousTab !== 'map-tab') {
        activateTab(previousTab);
        previousTab = null;
      }
    });
  }

  function activateTab(tabId) {
    document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
    document.querySelectorAll('.tab-pane').forEach(p => p.classList.add('hidden'));
    document.querySelector(`[data-tab="${tabId}"]`)?.classList.add('active');
    document.getElementById(tabId)?.classList.remove('hidden');

    if (tabId === 'graph-tab') {
      showGraph();
    } else {
      if (currentView === 'graph') hideGraph();
    }
    if (tabId === 'stats-tab') {
      fetchJSON('/api/stats').then(renderStatsFromData);
    }
  }

  // ────────────────────── Hash Routing ──────────────────────
  function handleHash() {
    const hash = window.location.hash;
    if (hash === '#graph') { activateTab('graph-tab'); return; }
    if (hash && hash.startsWith('#!')) {
      const nodeId = hash.slice(2);
      if (nodeId && nodeMap[nodeId]) selectNode(nodeId);
    }
  }

  // ────────────────────── Helpers ──────────────────────
  async function fetchJSON(url) { const r = await fetch(url); if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.json(); }
  function esc(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
  function escAttr(s) { return esc(s).replace(/'/g, '&#39;').replace(/"/g, '&quot;'); }
  function detailRow(l, v) { return `<dt>${esc(l)}</dt><dd>${v != null && v !== '' ? esc(String(v)) : '-'}</dd>`; }
  function detailRowHTML(l, v) { return `<dt>${esc(l)}</dt><dd>${v || '-'}</dd>`; }
  function progressBar(v) {
    const p = Math.min(Math.max(v * 100, 0), 100);
    return `<div class="progress-bar"><div class="progress-fill ${p < 60 ? 'bar-green' : p < 85 ? 'bar-yellow' : 'bar-red'}" style="width:${p}%"></div></div>`;
  }
  function formatUptime(s) {
    try { const d = Date.now() - new Date(s).getTime(); if (d < 0) return '?'; const dd = Math.floor(d/86400000), hh = Math.floor((d%86400000)/3600000); return dd > 0 ? `${dd}d ${hh}h` : `${hh}h ${Math.floor((d%3600000)/60000)}m`; } catch { return s; }
  }
  function formatDate(s) { if (!s) return '-'; try { const d = new Date(s); return isNaN(d) ? '-' : d.toLocaleString(); } catch { return '-'; } }
  function formatDistance(m) { return m < 1000 ? Math.round(m) + ' m' : (m / 1000).toFixed(1) + ' km'; }

  // ────────────────────── Public API ──────────────────────
  window.FFMap = { selectNode };
  document.addEventListener('DOMContentLoaded', init);
})();
