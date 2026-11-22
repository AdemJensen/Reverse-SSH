(async () => {
  const link = document.getElementById('latest-release');
  if (!link) return;

  try {
    const response = await fetch('https://api.github.com/repos/AdemJensen/Reverse-SSH/releases/latest', {
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
    });
    if (!response.ok) return;

    const release = await response.json();
    const tag = release?.tag_name;
    if (typeof tag === 'string' && /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/.test(tag)) {
      link.textContent = `${tag} 发布说明`;
    }
  } catch {
    // The static link still points to the latest release.
  }
})();
