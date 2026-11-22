// Apply the saved theme before first paint (see src/providers/theme.tsx).
// This is a file, not an inline script, so the page's Content-Security-Policy
// can stay at script-src 'self'.
try {
  var t = localStorage.getItem('theme')
  if (t === 'dark' || (t !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches)) document.documentElement.classList.add('dark')
} catch (e) {}
