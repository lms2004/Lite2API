/* Shared DOM behavior. Feature controllers own content and product decisions. */
(function (root) {
  'use strict';
  root.Lite2APIUI = Object.freeze({ create });

  function create(doc = document) {
    const content = new WeakMap();
    const openers = new WeakMap();
    const closing = new WeakMap();
    const actions = new WeakSet();
    let confirmation = null;
    let renderFrame = 0;
    let menuFrame = 0;

    function setHTML(node, html) {
      if (!node || content.get(node) === html) return;
      const key = (item, index) => item.dataset.uiKey || item.id || index;
      const details = new Map(
        [...node.querySelectorAll('details')].map((item, index) => [key(item, index), item.open]),
      );
      node.innerHTML = html;
      node.querySelectorAll('details').forEach((item, index) => {
        if (details.get(key(item, index))) item.open = true;
      });
      content.set(node, html);
    }

    // Keep live form controls in place. Replacing an editor during its change
    // event otherwise drops focus and can swallow the click that caused blur.
    function patchHTML(node, html) {
      if (!node || content.get(node) === html) return;
      const active = node.contains(doc.activeElement) ? doc.activeElement : null;
      const selection =
        typeof active?.selectionStart === 'number'
          ? [active.selectionStart, active.selectionEnd, active.selectionDirection]
          : null;
      const template = doc.createElement('template');
      template.innerHTML = html;
      patchChildren(node, template.content);
      if (active?.isConnected && !active.disabled && doc.activeElement !== active) {
        active.focus({ preventScroll: true });
        if (selection) active.setSelectionRange(...selection);
      }
      content.set(node, html);
      repositionMenus();
    }

    function nodeKey(node) {
      if (node.nodeType !== 1) return String(node.nodeType);
      for (const attribute of ['id', 'data-ui-key']) {
        if (node.hasAttribute(attribute))
          return `${node.nodeName}:${attribute}:${node.getAttribute(attribute)}`;
      }
      return node.nodeName;
    }

    function patchChildren(parent, source) {
      const available = new Map();
      const needed = new Map();
      for (const child of source.childNodes) {
        const key = nodeKey(child);
        needed.set(key, (needed.get(key) || 0) + 1);
      }
      for (const child of [...parent.childNodes]) {
        const key = nodeKey(child);
        if (!available.has(key)) available.set(key, []);
        const matches = available.get(key);
        if (matches.length < (needed.get(key) || 0)) matches.push(child);
        else child.remove();
      }
      let cursor = parent.firstChild;
      for (const next of source.childNodes) {
        const current = available.get(nodeKey(next))?.shift();
        if (current) {
          // Moving a focused node, even within its parent, can blur it. Stable
          // siblings therefore stay exactly where they are.
          if (current !== cursor) parent.insertBefore(current, cursor);
          patchNode(current, next);
          cursor = current.nextSibling;
        } else parent.insertBefore(next.cloneNode(true), cursor);
      }
      for (const children of available.values()) {
        for (const child of children) child.remove();
      }
    }

    function patchNode(node, next) {
      if (node.nodeType !== 1) {
        if (node.nodeValue !== next.nodeValue) node.nodeValue = next.nodeValue;
        return;
      }
      const control = node.matches('input,textarea,select');
      const editing =
        doc.activeElement === node ||
        (node.matches('input,textarea') &&
          node.value !== node.defaultValue &&
          !!node.closest('.compact-row-actions[open]'));
      const value = control ? (editing ? node.value : next.value) : undefined;
      const busy = actions.has(node);
      const preserve = (name) =>
        (name === 'open' && node.nodeName === 'DETAILS') ||
        (name === 'value' && editing && control) ||
        (name === 'style' && node.parentElement?.matches('.compact-row-actions,.route-target-picker')) ||
        (busy && (name === 'disabled' || name === 'aria-busy'));
      for (const attribute of [...node.attributes]) {
        if (!preserve(attribute.name) && !next.hasAttribute(attribute.name))
          node.removeAttribute(attribute.name);
      }
      for (const attribute of next.attributes) {
        if (!preserve(attribute.name) && node.getAttribute(attribute.name) !== attribute.value)
          node.setAttribute(attribute.name, attribute.value);
      }
      if (!busy) patchChildren(node, next);
      if (
        control &&
        node.value !== value &&
        (node.nodeName !== 'SELECT' || [...node.options].some((option) => option.value === value))
      )
        node.value = value;
      if (node.nodeName === 'INPUT' && node.checked !== next.checked) node.checked = next.checked;
    }

    function scheduleRender(render) {
      cancelAnimationFrame(renderFrame);
      renderFrame = requestAnimationFrame(() => {
        renderFrame = 0;
        render();
      });
    }

    function openDialog(id) {
      const dialog = doc.getElementById(id);
      clearTimeout(closing.get(dialog));
      closing.delete(dialog);
      dialog.removeAttribute('data-closing');
      if (dialog.open) return;
      openers.set(dialog, doc.activeElement);
      dialog.showModal();
      doc.body.classList.add('dialog-open');
    }

    function closeDialog(id) {
      const dialog = doc.getElementById(id);
      if (!dialog?.open || closing.has(dialog)) return;
      const finish = () => {
        closing.delete(dialog);
        dialog.removeAttribute('data-closing');
        dialog.close();
      };
      if (matchMedia('(prefers-reduced-motion: reduce)').matches) finish();
      else {
        dialog.setAttribute('data-closing', '');
        closing.set(dialog, setTimeout(finish, 140));
      }
    }

    function confirmAction({ title, message, confirmLabel = '确认', destructive = false }) {
      if (confirmation) return Promise.resolve(false);
      const dialog = doc.getElementById('confirmDialog');
      doc.getElementById('confirmTitle').textContent = title;
      doc.getElementById('confirmMessage').textContent = message;
      const button = doc.getElementById('confirmAccept');
      button.textContent = confirmLabel;
      button.className = destructive ? 'primary destructive' : 'primary';
      return new Promise((resolve) => {
        confirmation = { resolve, accepted: false };
        openDialog(dialog.id);
      });
    }

    async function runAction(button, action, label) {
      if (!button || actions.has(button)) return;
      actions.add(button);
      const text = button.textContent;
      const disabled = button.disabled;
      button.disabled = true;
      button.setAttribute('aria-busy', 'true');
      if (label) button.textContent = label;
      try {
        return await action();
      } finally {
        actions.delete(button);
        button.disabled = disabled;
        button.removeAttribute('aria-busy');
        if (label) button.textContent = text;
      }
    }

    function closeMenus(except = null) {
      doc.querySelectorAll('.compact-row-actions[open], .route-target-picker[open]').forEach((menu) => {
        if (menu !== except) menu.open = false;
      });
    }

    function placeMenu(menu) {
      const trigger = menu.querySelector('summary');
      const panel = menu.querySelector(':scope > div');
      if (!trigger || !panel || !menu.open) return;
      const rect = trigger.getBoundingClientRect();
      const viewport = window.visualViewport;
      const width = viewport?.width || window.innerWidth;
      const height = viewport?.height || window.innerHeight;
      panel.style.position = 'fixed';
      panel.style.right = 'auto';
      panel.style.bottom = 'auto';
      panel.style.width =
        Math.min(menu.classList.contains('route-target-picker') ? 380 : 272, width - 24) + 'px';
      panel.style.maxHeight = Math.max(120, height - 140) + 'px';
      panel.style.left =
        Math.max(12, Math.min(rect.right - panel.offsetWidth, width - panel.offsetWidth - 12)) + 'px';
      panel.style.top =
        Math.max(
          12,
          Math.min(
            height - panel.offsetHeight - 12,
            rect.bottom + 8 + panel.offsetHeight > height - 80
              ? rect.top - panel.offsetHeight - 8
              : rect.bottom + 8,
          ),
        ) + 'px';
    }

    function repositionMenus() {
      if (menuFrame || !doc.querySelector('.compact-row-actions[open], .route-target-picker[open]')) return;
      menuFrame = requestAnimationFrame(() => {
        menuFrame = 0;
        doc.querySelectorAll('.compact-row-actions[open], .route-target-picker[open]').forEach(placeMenu);
      });
    }

    function bind() {
      doc.getElementById('confirmForm').addEventListener('submit', (event) => {
        event.preventDefault();
        if (!confirmation) return;
        confirmation.accepted = true;
        closeDialog('confirmDialog');
      });
      doc
        .querySelectorAll('[data-close-dialog]')
        .forEach((button) => button.addEventListener('click', () => closeDialog(button.dataset.closeDialog)));
      doc.querySelectorAll('dialog').forEach((dialog) => {
        dialog.addEventListener('cancel', (event) => {
          event.preventDefault();
          closeDialog(dialog.id);
        });
        dialog.addEventListener('click', (event) => {
          const box = dialog.getBoundingClientRect();
          if (
            event.target === dialog &&
            (event.clientX < box.left ||
              event.clientX > box.right ||
              event.clientY < box.top ||
              event.clientY > box.bottom)
          )
            closeDialog(dialog.id);
        });
        dialog.addEventListener('close', () => {
          closing.delete(dialog);
          const anyOpen = doc.querySelector('dialog[open]');
          doc.body.classList.toggle('dialog-open', !!anyOpen);
          let opener = openers.get(dialog);
          if (opener?.disabled || !opener?.getClientRects().length)
            opener = opener?.closest('details')?.querySelector('summary');
          if (!anyOpen && opener?.isConnected && opener.getClientRects().length)
            opener.focus({ preventScroll: true });
          if (dialog.id === 'confirmDialog' && confirmation) {
            const { resolve, accepted } = confirmation;
            confirmation = null;
            resolve(accepted);
          }
        });
      });
      doc.addEventListener(
        'toggle',
        (event) => {
          const menu = event.target;
          if (!menu.matches?.('.compact-row-actions, .route-target-picker') || !menu.open) return;
          closeMenus(menu);
          placeMenu(menu);
        },
        true,
      );
      doc.addEventListener('click', (event) => {
        const summary = event.target.closest(
          '.compact-row-actions > summary, .route-target-picker > summary',
        );
        if (summary) {
          // Native toggle events are queued. Position synchronously so the first
          // visible frame cannot use coordinates left over from another viewport.
          event.preventDefault();
          const menu = summary.parentElement;
          menu.open = !menu.open;
          if (menu.open) {
            closeMenus(menu);
            placeMenu(menu);
          }
          return;
        }
        if (!event.target.closest('.compact-row-actions, .route-target-picker')) closeMenus();
      });
      doc.addEventListener('keydown', (event) => {
        const menu = event.target.closest('.compact-row-actions[open], .route-target-picker[open]');
        if (!menu) return;
        if (event.key === 'Escape') {
          event.preventDefault();
          event.stopPropagation();
          menu.open = false;
          menu.querySelector('summary').focus();
        }
        if (
          !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) ||
          event.target.matches('input,select')
        )
          return;
        const items = [...menu.querySelectorAll('button:not(:disabled), input:not(:disabled)')];
        if (!items.length) return;
        event.preventDefault();
        const index = items.indexOf(doc.activeElement);
        const next =
          event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? items.length - 1
              : (index + (event.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length;
        items[next].focus();
      });
      window.addEventListener('resize', repositionMenus, { passive: true });
      doc.addEventListener('scroll', repositionMenus, { passive: true, capture: true });
    }

    async function copy(text) {
      if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
      const input = doc.createElement('textarea');
      input.value = text;
      input.className = 'clipboard-fallback';
      (doc.querySelector('dialog[open]') || doc.body).append(input);
      const active = doc.activeElement;
      input.select();
      try {
        if (!doc.execCommand('copy')) throw new Error('无法自动复制，请选中文本手动复制。');
      } finally {
        input.remove();
        active?.focus({ preventScroll: true });
      }
    }
    return Object.freeze({
      setHTML,
      patchHTML,
      scheduleRender,
      openDialog,
      closeDialog,
      confirm: confirmAction,
      runAction,
      closeMenus,
      bind,
      copy,
    });
  }
})(globalThis);
