const paths = {
  plus: "M12 5v14M5 12h14",
  file: "M14 3H6.5A1.5 1.5 0 0 0 5 4.5v15A1.5 1.5 0 0 0 6.5 21h11a1.5 1.5 0 0 0 1.5-1.5V8zM14 3v5h5",
  image: "M4 5h16v14H4zM4 16l4.5-4.5 4 4 2.5-2.5L20 18M15.5 9.5h.01",
  film: "M4 5h16v14H4zM8 5v14M16 5v14M4 9.5h4M16 9.5h4M4 14.5h4M16 14.5h4",
  music: "M9 18V6l11-2v12M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0zM20 16a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z",
  archive: "M4 5h16v4H4zM5.5 9v10h13V9M10 13h4",
  save: "M12 4v11M7.5 10.5L12 15l4.5-4.5M5 19.5h14",
  open: "M14 4h6v6M20 4l-9 9M18 14v4.5a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 4 18.5v-11A1.5 1.5 0 0 1 5.5 6H10",
  folder: "M3 7.5A1.5 1.5 0 0 1 4.5 6H9l2 2h8.5A1.5 1.5 0 0 1 21 9.5v8a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z",
  sliders: "M4 7h9M17 7h3M4 17h3M11 17h9M15 5v4M9 15v4",
  menu: "M4 7h16M4 12h16M4 17h16",
  down: "M7 10l5 5 5-5",
  right: "M10 7l5 5-5 5",
  arrowUp: "M12 19V6M6.5 11.5L12 6l5.5 5.5",
  arrowDown: "M12 5v13M6.5 12.5L12 18l5.5-5.5",
  arrowRight: "M5 12h13M12.5 6.5L18 12l-5.5 5.5",
  check: "M5 12.5l4.5 4.5L19 7.5",
  x: "M6.5 6.5l11 11M17.5 6.5l-11 11",
  copy: "M9 9h10v10H9zM5 15V5h10",
  home: "M4 11l8-6.5 8 6.5M6 9.5V19h12V9.5",
  alert: "M12 9v4M12 16.5v.01M10.3 4.2L2.8 17.5A2 2 0 0 0 4.5 20.5h15a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0z",
  drive: "M3 13h18M5 13l2-7h10l2 7v5H5zM8 16h.01",
  edit: "M12 20h8M15.5 4.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4z",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
  sun: "M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2.5v2M12 19.5v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2.5 12h2M19.5 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4",
  moon: "M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z",
  monitor: "M3 5h18v11H3zM8 20h8M12 16v4",
  chip: "M7 7h10v10H7zM10 3v4M14 3v4M10 17v4M14 17v4M3 10h4M3 14h4M17 10h4M17 14h4",
  layers: "M12 3l9 5-9 5-9-5zM3 13l9 5 9-5",
  map: "M9 4L3 6v14l6-2 6 2 6-2V4l-6 2zM9 4v14M15 6v14",
  code: "M8 7l-5 5 5 5M16 7l5 5-5 5",
  square: "M7 7h10v10H7z",
  steps: "M5 6h.01M5 12h.01M5 18h.01M9 6h10M9 12h10M9 18h10",
  server: "M4 4.5h16v6H4zM4 13.5h16v6H4zM8 7.5h.01M8 16.5h.01",
  terminal: "M5 7l5 5-5 5M12.5 17H19",
  key: "M14.5 9.5a3.5 3.5 0 1 0-1.2 2.6L20 18.8V21h-2.5v-1.5H16V18h-1.5l-1.2-1.2",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M5.5 11h13v9.5h-13z",
  gauge: "M5 18a8 8 0 1 1 14 0M12 14l3.5-4.5",
  plug: "M9 3v5M15 3v5M6.5 8h11v3a5.5 5.5 0 0 1-11 0zM12 16.5V21",
} as const;

export type IconName = keyof typeof paths;

export function Icon({ name, size = 16, className }: { name: IconName; size?: number; className?: string }) {
  return (
    <svg className={className ? "i " + className : "i"} width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
      <path d={paths[name]} />
    </svg>
  );
}
