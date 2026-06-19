// nq-ui: file upload queue
(function() {
  var ctx = Module.nqOverlayCtx;
  if (!ctx) return;

  function formatBytes(bytes) {
    var n = Number(bytes || 0);
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    return (n / (1024 * 1024)).toFixed(1) + ' MB';
  }

  function readFileAsUint8(file, onProgress) {
    return new Promise(function(resolve, reject) {
      var reader = new FileReader();
      reader.onerror = function() {
        reject(reader.error || new Error('read failed'));
      };
      reader.onprogress = onProgress || null;
      reader.onload = function(e) {
        var result = e && e.target ? e.target.result : null;
        if (!(result instanceof ArrayBuffer)) {
          reject(new Error('Unexpected file reader result'));
          return;
        }
        resolve(new Uint8Array(result));
      };
      reader.readAsArrayBuffer(file);
    });
  }

  async function uploadFile(file, index, total, options) {
    var opts = options || {};
    var allowedExts = opts.extensions || ctx.USER_EXTS;
    var invalidText = opts.invalidText || ('Quake ' + ctx.USER_FILE_DESC + ' only');
    var dir = String(opts.dir || ctx.getUploadDir());
    var dirPath;
    var rawName;
    var ext;
    var isCdUpload;
    var name;
    var dstPath;
    var label;
    var data;

    rawName = String(file && file.name || '').split(/[\\/]/).pop();
    ext = rawName.slice(rawName.lastIndexOf('.') + 1).toLowerCase();
    if (!rawName || allowedExts.indexOf(ext) < 0) {
      ctx.showErrorMessage(invalidText, 3000);
      return false;
    }
    ctx.clearStatusMessage('upload-progress');

    dirPath = dir.replace(/\/$/, '');
    isCdUpload = typeof ctx.isCdDir === 'function' && ctx.isCdDir(dir);
    name = isCdUpload ? rawName : rawName.toLowerCase();
    dstPath = dir + name;
    label = 'Uploading ' + name + ' (' + index + '/' + total + ')';

    if (ctx.safeStat(dstPath)) {
      if (!await ctx.confirmAsync('Overwrite ' + dstPath + '?', 'overwrite'))
        return null;
    }

    try {
      ctx.showWarningMessage(label + ' 0%', 0, true, { key: 'upload-progress' });
      data = await readFileAsUint8(file, function(e) {
        var pct;
        if (!e || !e.lengthComputable || e.total <= 0) {
          ctx.showWarningMessage(label + '...', 0, true, { key: 'upload-progress' });
          return;
        }
        pct = Math.max(0, Math.min(100, Math.round((e.loaded * 100) / e.total)));
        ctx.showWarningMessage(label + ' ' + pct + '%', 0, true, { key: 'upload-progress' });
      });
    } catch (err) {
      ctx.showErrorMessage('Upload failed for ' + name, 3500);
      console.error('Upload read failed:', err);
      return false;
    }

    try {
      ctx.safeMkdirTree(dirPath);
      ctx.safeUnlink(dstPath);
      FS.writeFile(dstPath, data);
      return true;
    } catch (err2) {
      ctx.showErrorMessage('Upload failed for ' + name, 3500);
      console.error('Upload write failed:', err2);
      return false;
    }
  }

  async function processUploads(files, options) {
    var queue = Array.from(files || []);
    var opts = options || {};
    var refreshOnSuccess = opts.refreshOnSuccess !== false;
    var uploaded = 0;
    var syncErr;
    var totalBytes;
    var i;

    if (ctx.uploadBusy || !queue.length) return;

    ctx.setUploadBusyState(true);
    ctx.clearStatusMessage('upload-progress');

    try {
      for (i = 0; i < queue.length; i++) {
        var ok = await uploadFile(queue[i], i + 1, queue.length, opts);
        if (ok === true) uploaded++;
      }

      if (uploaded > 0) {
        ctx.showWarningMessage('Syncing ' + uploaded + ' file(s) to storage...', 0, true, { key: 'upload-progress' });
        syncErr = await new Promise(function(resolve) {
          try {
            FS.syncfs(false, function(err) { resolve(err || null); });
          } catch (e) {
            resolve(e || null);
          }
        });
        if (syncErr) {
          ctx.showErrorMessage('Storage sync failed', 3500);
          console.error('Upload sync failed:', syncErr);
        }
      }

      if (uploaded > 0 && refreshOnSuccess) ctx.refresh();
      ctx.clearStatusMessage('upload-progress');

      if (uploaded > 0) {
        totalBytes = queue.reduce(function(acc, file) { return acc + Number(file.size || 0); }, 0);
        ctx.showInfoMessage('Uploaded ' + uploaded + ' file(s) (' + formatBytes(totalBytes) + ')', 2500);
      }
    } finally {
      ctx.clearStatusMessage('upload-progress');
      ctx.setUploadBusyState(false);
    }
  }

  ctx.processUploads = processUploads;
})();
