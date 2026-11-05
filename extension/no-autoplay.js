let blocking = true;

chrome.storage.local.get("blockAutoplay").then((s) => {
  blocking = s.blockAutoplay !== false;
});

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "local" && "blockAutoplay" in changes) blocking = changes.blockAutoplay.newValue !== false;
});

document.addEventListener(
  "play",
  (e) => {
    if (blocking && e.target instanceof HTMLMediaElement && !navigator.userActivation.hasBeenActive) {
      e.target.pause();
    }
  },
  true,
);
