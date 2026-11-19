export function IconDefs() {
  return (
    <svg className="defs" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
      <symbol id="wave" viewBox="0 0 24 24">
        <path d="M2 12h3l3-6 4 12 4-12 3 6h3" />
      </symbol>
      <symbol id="plus" viewBox="0 0 24 24">
        <path d="M12 5v14M5 12h14" />
      </symbol>
      <symbol id="inbox" viewBox="0 0 24 24">
        <path d="M4 13l3-8h10l3 8v6H4zM4 13h5l1 2h4l1-2h5" />
      </symbol>
      <symbol id="audio" viewBox="0 0 24 24">
        <path d="M4 14v-2a8 8 0 0116 0v2M4 13h2a2 2 0 012 2v3a2 2 0 01-2 2H4zM20 13h-2a2 2 0 00-2 2v3a2 2 0 002 2h2z" />
      </symbol>
      <symbol id="tag" viewBox="0 0 24 24">
        <path d="M3 4h8l9 9-7 7-10-9zM7.5 8h.01" />
      </symbol>
      <symbol id="chevron" viewBox="0 0 24 24">
        <path d="m9 6 6 6-6 6" />
      </symbol>
      <symbol id="back" viewBox="0 0 24 24">
        <path d="M19 12H5m6-6-6 6 6 6" />
      </symbol>
      <symbol id="play" viewBox="0 0 24 24">
        <path d="m8 5 12 7-12 7z" fill="currentColor" stroke="none" />
      </symbol>
      <symbol id="pause" viewBox="0 0 24 24">
        <path d="M9 5v14m6-14v14" strokeWidth="3" />
      </symbol>
      <symbol id="export" viewBox="0 0 24 24">
        <path d="M12 3v12m-4-4 4 4 4-4M5 16v4h14v-4" />
      </symbol>
      <symbol id="download" viewBox="0 0 24 24">
        <path d="M12 4v11m-4-4 4 4 4-4M5 19h14" />
      </symbol>
      <symbol id="rewind" viewBox="0 0 24 24">
        <path d="M4 10a8 8 0 11.4 5M4 4v6h6m0 2 2-1v6m3-5h3l-2 2a1.5 1.5 0 11-1 2" />
      </symbol>
      <symbol id="forward" viewBox="0 0 24 24">
        <path d="M20 10a8 8 0 10-.4 5M20 4v6h-6m-6 2h3l-2 2a1.5 1.5 0 11-1 2m6-4h2v5h-2z" />
      </symbol>
      <symbol id="close" viewBox="0 0 24 24">
        <path d="m6 6 12 12M18 6 6 18" />
      </symbol>
      <symbol id="check" viewBox="0 0 24 24">
        <path d="m5 12 4 4L19 6" />
      </symbol>
      <symbol id="search" viewBox="0 0 24 24">
        <circle cx="10.5" cy="10.5" r="5.5" />
        <path d="m15 15 4 4" />
      </symbol>
      <symbol id="spark" viewBox="0 0 24 24">
        <path d="m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5z" />
      </symbol>
      <symbol id="cc" viewBox="0 0 24 24">
        <rect x="3" y="5" width="18" height="14" rx="2" />
        <path d="M10 10H7v4h3m7-4h-3v4h3" />
      </symbol>
      <symbol id="refresh" viewBox="0 0 24 24">
        <path d="M20 11a8 8 0 10-2.3 6M20 5v6h-6" />
      </symbol>
      <symbol id="cover-art" viewBox="0 0 64 64">
        <path d="M-12 45C10 40 16 12 36 18S60 51 77 33M-12 48C10 44 17 16 36 22S60 55 77 36M-12 51C9 48 18 20 36 26S60 59 77 39M-12 54C9 52 19 24 36 30S61 63 77 42M-12 57C8 56 20 28 36 34S61 67 77 45M-12 60C8 60 21 32 36 38S61 71 77 48" />
      </symbol>
    </svg>
  )
}

export function Icon({ name }: { name: string }) {
  return (
    <svg aria-hidden="true">
      <use href={`#${name}`} />
    </svg>
  )
}
