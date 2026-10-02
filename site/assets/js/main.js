// ===== COPY BUTTON =====
function initCopy() {
  document.querySelectorAll('[data-copy]').forEach(btn => {
    btn.addEventListener('click', () => {
      const target = btn.dataset.copy;
      const text = target
        ? (document.getElementById(target)?.innerText ?? target)
        : btn.dataset.copyText ?? '';
      navigator.clipboard.writeText(text.trim()).then(() => {
        const orig = btn.textContent;
        btn.textContent = 'Copied!';
        btn.classList.add('copied');
        setTimeout(() => { btn.textContent = orig; btn.classList.remove('copied'); }, 2000);
      });
    });
  });
}

// ===== TABS =====
function initTabs() {
  document.querySelectorAll('.tabs').forEach(tabBar => {
    const panel = tabBar.closest('.tab-group');
    if (!panel) return;
    const btns = tabBar.querySelectorAll('.tab-btn');
    const panels = panel.querySelectorAll('.tab-panel');
    btns.forEach((btn, i) => {
      btn.addEventListener('click', () => {
        btns.forEach(b => b.classList.remove('active'));
        panels.forEach(p => p.classList.remove('active'));
        btn.classList.add('active');
        panels[i]?.classList.add('active');
      });
    });
  });
}

// ===== HAMBURGER NAV =====
function initNav() {
  const burger = document.querySelector('.nav-burger');
  const links = document.querySelector('.nav-links');
  if (!burger || !links) return;
  burger.addEventListener('click', () => links.classList.toggle('open'));
  document.addEventListener('click', e => {
    if (!burger.contains(e.target) && !links.contains(e.target)) links.classList.remove('open');
  });
}

// ===== ACTIVE SIDEBAR LINK (docs pages) =====
function initDocsSidebar() {
  const sidebar = document.querySelector('.docs-sidebar');
  if (!sidebar) return;
  const path = window.location.pathname.split('/').pop() || 'index.html';
  sidebar.querySelectorAll('a').forEach(a => {
    if (a.getAttribute('href') === path) a.classList.add('active');
  });
}

// ===== SCROLL SPY (simple) =====
function initScrollSpy() {
  const sections = document.querySelectorAll('section[id]');
  const navLinks = document.querySelectorAll('.nav-links a[href^="#"]');
  if (!sections.length || !navLinks.length) return;
  const obs = new IntersectionObserver(entries => {
    entries.forEach(e => {
      if (e.isIntersecting) {
        navLinks.forEach(l => l.classList.remove('active'));
        const link = document.querySelector(`.nav-links a[href="#${e.target.id}"]`);
        link?.classList.add('active');
      }
    });
  }, { rootMargin: '-30% 0px -60% 0px' });
  sections.forEach(s => obs.observe(s));
}

// ===== INSTALL CMD COPY =====
function initInstallCopy() {
  const btn = document.getElementById('installCopy');
  if (!btn) return;
  btn.addEventListener('click', () => {
    const cmd = document.getElementById('installCmd')?.textContent ?? '';
    navigator.clipboard.writeText(cmd.trim()).then(() => {
      btn.textContent = '✓ Copied!';
      btn.classList.add('copied');
      setTimeout(() => { btn.textContent = 'Copy'; btn.classList.remove('copied'); }, 2000);
    });
  });
}

document.addEventListener('DOMContentLoaded', () => {
  initCopy();
  initTabs();
  initNav();
  initDocsSidebar();
  initScrollSpy();
  initInstallCopy();
});
