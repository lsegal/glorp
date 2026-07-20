import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, Check, Circle, FolderGit2, Radio, Terminal, X } from "lucide-react";
import "./index.css";

const emptyState = { snapshot: { Jobs: [], Targets: [], IssueCounts: {} }, logs: [] };

function useDashboardState() {
  const [state, setState] = useState(emptyState);
  const [connected, setConnected] = useState(true);
  useEffect(() => {
    let stopped = false;
    const refresh = async () => {
      try {
        const response = await fetch("/api/state", { cache: "no-store" });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const next = await response.json();
        if (!stopped) {
          setState(next);
          setConnected(true);
        }
      } catch {
        if (!stopped) setConnected(false);
      }
    };
    refresh();
    const timer = window.setInterval(refresh, 1000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, []);
  return [state, connected];
}

function ScrollViewport({ value, empty = "waiting for output...", label }) {
  const viewport = useRef(null);
  const follow = useRef(true);
	const [showMore, setShowMore] = useState(false);
  useEffect(() => {
    const node = viewport.current;
    if (node && follow.current) node.scrollTop = node.scrollHeight;
  }, [value]);
  const onScroll = () => {
    const node = viewport.current;
    follow.current = node.scrollHeight - node.scrollTop - node.clientHeight < 8;
		setShowMore(!follow.current);
  };
  return (
    <div className="viewport-wrap">
      <pre ref={viewport} onScroll={onScroll} aria-label={label} className="viewport">
        {value || empty}
      </pre>
		{showMore && (
        <button className="more" onClick={() => {
          follow.current = true;
			setShowMore(false);
          viewport.current?.scrollTo({ top: viewport.current.scrollHeight, behavior: "smooth" });
        }}>more ↓</button>
      )}
    </div>
  );
}

function JobIcon({ status }) {
  if (status === "complete") return <Check className="status-icon complete" aria-label="complete" />;
  if (status === "failed") return <X className="status-icon failed" aria-label="failed" />;
  if (status === "active") return <Activity className="status-icon active" aria-label="active" />;
  return <Circle className="status-icon queued" aria-label="queued" />;
}

function JobCard({ job }) {
  return (
    <article className="card">
      <header className="card-header">
        <JobIcon status={job.Status} />
        <h2>#{job.Number} {job.Title}</h2>
      </header>
      <div className="meta" title={job.CheckoutDirectory}><FolderGit2 /> checkout: {job.CheckoutDirectory || "pending"}</div>
      <div className="meta" title={job.SessionID}><Terminal /> session: {job.SessionID || "pending"}</div>
      <ScrollViewport value={job.Log} label={`Agent output for issue ${job.Number}`} />
    </article>
  );
}

function StatusBar({ snapshot, connected }) {
  const active = (snapshot.Running || 0) + (snapshot.Queued || 0);
  const idle = Math.max(0, (snapshot.Concurrency || 0) - active);
  const total = (snapshot.Completed || 0) + (snapshot.Failed || 0) + active;
	const interval = snapshot.Interval ? `${snapshot.Interval / 1_000_000_000}s` : "—";
	const delivery = snapshot.UseWebhooks ? "push" : `polling every ${interval}`;
  const targets = (snapshot.Targets || []).map((target) => `${target} (${snapshot.IssueCounts?.[target] || 0} issues)`).join(", ");
  return (
    <footer className="status-bar">
      <div className="status-cell jobs">jobs: <span className="idle">{idle}</span> idle <span className="active-text">{active}</span> active <span className="total">{total}</span> total</div>
      <div className="status-cell quota">quota: {snapshot.Quota || "unavailable"}</div>
      <div className="status-cell delivery"><Radio /> {delivery}{!connected && " (offline)"}</div>
      <div className="status-cell targets">targets: {targets || "waiting..."}</div>
    </footer>
  );
}

function App() {
  const [state, connected] = useDashboardState();
  const snapshot = state.snapshot || emptyState.snapshot;
  return (
    <main>
      <div className="masthead">
        <div>
          <p className="eyebrow">Git Loop fOr Robot Patchers</p>
          <h1>glorp <span>dashboard</span></h1>
        </div>
        <div className={`connection ${connected ? "online" : "offline"}`}><i />{connected ? "live" : "reconnecting"}</div>
      </div>
      <section className="job-grid" aria-label="Agent jobs">
        {(snapshot.Jobs || []).length ? snapshot.Jobs.map((job) => <JobCard key={job.Number} job={job} />) : (
          <div className="empty-jobs"><Activity /><span>Waiting for agent jobs</span></div>
        )}
      </section>
      <section className="log-card">
        <div className="log-title"><Terminal /> Logs</div>
        <ScrollViewport value={(state.logs || []).join("\n")} empty="waiting for daemon logs..." label="Daemon logs" />
      </section>
      <StatusBar snapshot={snapshot} connected={connected} />
    </main>
  );
}

createRoot(document.getElementById("root")).render(<App />);
