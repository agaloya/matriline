// add.js - the web page's "add" form: files and folders chosen or dropped one after another
// add up in a list (a file field alone replaces its selection each time), dropped folders
// keep their sub-folders, and everything is sent together. Without this script the form
// still works, one selection at a time.
(function () {
  var form = document.querySelector('form[action="/add"]');
  if (!form || !window.FormData || !window.fetch) return;
  var zone = form.querySelector('.drop');
  var inputs = form.querySelectorAll('input[type=file]');
  var list = document.createElement('ul');
  list.className = 'picked';
  zone.parentNode.insertBefore(list, zone.nextSibling);
  var picked = [];
  function wanted(name) { return /\.(inp|xyz)$/i.test(name); }
  function add(file, path) {
    if (!wanted(file.name)) return;
    for (var i = 0; i < picked.length; i++) if (picked[i].path === path) return;
    picked.push({ file: file, path: path });
  }
  function show() {
    list.textContent = '';
    picked.forEach(function (p) {
      var li = document.createElement('li');
      li.textContent = p.path;
      list.appendChild(li);
    });
    zone.querySelector('.hint').textContent = picked.length + ' ' + (zone.dataset.count || 'file(s)');
  }
  inputs.forEach(function (inp) {
    inp.addEventListener('change', function () {
      for (var i = 0; i < inp.files.length; i++) {
        var f = inp.files[i];
        add(f, f.webkitRelativePath || f.name);
      }
      inp.value = '';
      show();
    });
  });
  function walk(entry, prefix) {
    return new Promise(function (done) {
      if (entry.isFile) {
        entry.file(function (f) { add(f, prefix + f.name); done(); }, done);
      } else if (entry.isDirectory) {
        var reader = entry.createReader(), all = [];
        (function more() {
          reader.readEntries(function (batch) {
            if (!batch.length) {
              Promise.all(all.map(function (e) { return walk(e, prefix + entry.name + '/'); })).then(done);
              return;
            }
            all = all.concat(Array.prototype.slice.call(batch));
            more();
          }, done);
        })();
      } else done();
    });
  }
  zone.addEventListener('dragover', function (e) { e.preventDefault(); });
  zone.addEventListener('drop', function (e) {
    e.preventDefault();
    var entries = [];
    for (var i = 0; i < e.dataTransfer.items.length; i++) {
      var it = e.dataTransfer.items[i];
      var en = it.webkitGetAsEntry && it.webkitGetAsEntry();
      if (en) entries.push(en);
      else if (it.kind === 'file') { var f = it.getAsFile(); if (f) add(f, f.name); }
    }
    Promise.all(entries.map(function (en) { return walk(en, ''); })).then(show);
  });
  form.addEventListener('submit', function (e) {
    if (!picked.length) return;
    e.preventDefault();
    var fd = new FormData(form);
    fd.delete('files');
    fd.delete('folder');
    picked.forEach(function (p) { fd.append('files', p.file, p.path); });
    var hint = zone.querySelector('.hint');
    // over the server's limit the browser usually sees a reset, not its answer: say it first
    var total = picked.reduce(function (n, p) { return n + p.file.size; }, 0);
    if (total > Number(zone.dataset.max)) { hint.textContent = zone.dataset.toolarge; return; }
    hint.textContent = zone.dataset.sending || 'sending...';
    fetch('/add', { method: 'POST', body: fd, credentials: 'same-origin' })
      .then(function (r) { return r.text().then(function (t) { return { ok: r.ok, text: t }; }); })
      .then(function (x) {
        if (!x.ok) { hint.textContent = x.text; return; } // e.g. too large: say why, keep the list
        document.open(); document.write(x.text); document.close();
      })
      .catch(function (err) { hint.textContent = String(err); });
  });
})();
