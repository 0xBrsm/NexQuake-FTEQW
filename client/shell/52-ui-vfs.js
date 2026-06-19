// nq-ui: directory tabs, file list, editor, and file operations
(function() {
  var ctx = Module.nqOverlayCtx;
  if (!ctx) return;

  var FILE_DELETE_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>';
  var FILE_EXEC_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.5 9a9 9 0 0 1 14.8-3.4L23 10"/><path d="M20.5 15A9 9 0 0 1 5.7 18.4L1 14"/></svg>';

  function getDirs() {
    var baseDir = ctx.getBaseGameDir();
    var dirs = [baseDir];
    var seen = {};
    var installed = Module.nexquakeInstalledManifests || {};

    seen[baseDir] = true;

    function add(dir) {
      if (!dir || dir === ctx.USERFS + '/' || dir === ctx.CD_DIR || seen[dir]) return;
      seen[dir] = true;
      dirs.push(dir);
    }

    Object.keys(installed).forEach(function(mod) {
      if (installed[mod]) add('/' + mod + '/');
    });

    if (typeof FS !== 'undefined') {
      ctx.safeReadDir(ctx.USERFS).forEach(function(name) {
        var st;
        if (name === '.' || name === '..') return;
        st = ctx.safeStat(ctx.USERFS + '/' + name);
        if (st && FS.isDir(st.mode)) add('/' + name + '/');
      });
    }
    dirs.sort(function(a, b) { return a === baseDir ? -1 : b === baseDir ? 1 : a.localeCompare(b); });
    return dirs;
  }

  function buildTabs() {
    var dirs = getDirs();
    var labels = ['GAME'];
    ctx.tabs.innerHTML = '';

    dirs.forEach(function(dir) {
      var btn = document.createElement('button');
      var name = dir.replace(/^\/|\/$/g, '');
      btn.className = 'nq-tab' + (ctx.currentDir === dir ? ' active' : '');
      btn.textContent = name;
      btn.dataset.dir = dir;
      ctx.tabs.appendChild(btn);
      labels.push(name);
    });

    ctx.setTabsOpenWidth(labels);
  }

  function collectFiles(dir, includeFile) {
    var out = [];
    ctx.safeReadDir(dir).forEach(function(name) {
      var path;
      var st;
      if (name === '.' || name === '..') return;
      path = dir + (dir === '/' ? '' : '/') + name;
      st = ctx.safeStat(path);
      if (!st) return;
      if (FS.isDir(st.mode)) out = out.concat(collectFiles(path, includeFile));
      else if (FS.isFile(st.mode) && (!includeFile || includeFile(path))) out.push(path);
    });
    return out;
  }

  function isCfg(path) {
    return path.toLowerCase().endsWith('.cfg');
  }

  function toFsPath(dir, displayPath) {
    var rel = String(displayPath || '').trim();
    var root;
    var prefix;
    if (!rel || !rel.startsWith('/'))
      return rel;
    if (!ctx.isCdDir(dir)) {
      root = String(ctx.USERFS || '').replace(/\/$/, '');
      return root ? (root + rel) : rel;
    }
    root = String(ctx.CD_USERFS || '').replace(/\/$/, '');
    prefix = String(ctx.CD_DIR || '/cd/');
    if (!root || rel.indexOf(prefix) !== 0)
      return rel;
    return root + '/' + rel.slice(prefix.length).replace(/^\/+/, '');
  }

  function openEditor(displayPath) {
    try {
      var data = FS.readFile(displayPath, { encoding: 'utf8' });
      ctx.editingFile = { display: displayPath, backup: displayPath };
      ctx.editorPath.textContent = displayPath;
      ctx.editorText.value = data;
      ctx.editor.classList.add('open');
      ctx.editorText.setSelectionRange(0, 0);
      ctx.editorText.scrollTop = 0;
      ctx.editorText.focus();
    } catch (e) {
      console.error('Failed to read file for editor:', e);
    }
  }

  function toExecCfgArg(displayPath) {
    var path = String(displayPath || '').trim();
    if (!path)
      return '';
    if (path.indexOf(ctx.currentDir) === 0)
      path = path.slice(ctx.currentDir.length);
    path = path.replace(/^\/+/, '');
    if (!path || !isCfg(path))
      return '';
    if (/[\r\n"]/.test(path))
      return '';
    return '"' + path + '"';
  }

  function execCfgFile(displayPath) {
    var arg;
    var command;
    if (ctx.isCdDir(ctx.currentDir) || !isCfg(displayPath))
      return;
    arg = toExecCfgArg(displayPath);
    if (!arg) {
      ctx.showErrorMessage('Invalid cfg path', 2000);
      return;
    }
    command = 'exec ' + arg;
    if (!nqWasmExecCommand(command)) {
      ctx.showErrorMessage('Failed to run: ' + command, 3000);
      console.error('Exec cfg failed: wasm command bridge unavailable');
      return;
    }
    ctx.showInfoMessage(command, 1300);
  }

  function closeEditor() {
    if (!ctx.editor.classList.contains('open')) return false;
    ctx.editor.classList.remove('open');
    ctx.editingFile = null;
    ctx.editorText.value = '';
    return true;
  }

  function syncAndRefresh() {
    ctx.safeSyncFS();
    ctx.refresh();
  }

  async function deleteFile(displayPath) {
    var fsPath;
    var runtimeState;
    var stateForDeletedTrack;

    try {
      fsPath = toFsPath(ctx.currentDir, displayPath);
      if (ctx.isCdDir(ctx.currentDir)) {
        runtimeState = ctx.getCdRuntimeState();
        stateForDeletedTrack = ctx.getCdTrackButtonState(displayPath, runtimeState);
        if (stateForDeletedTrack.isCurrentTrack && runtimeState.state !== 'stopped') {
          try {
            await ctx.runCdCommand('stop');
          } catch (stopErr) {
            console.error('CD stop failed during delete:', stopErr);
          }
        }
      }
      // Direct unlink, not ctx.safeUnlink: that helper swallows errors, which
      // made this catch unreachable — a failed delete showed no feedback and
      // the file silently reappeared on refresh.
      FS.unlink(fsPath);
      syncAndRefresh();
    } catch (e) {
      ctx.showErrorMessage('Delete failed', 3000);
      console.error('Delete failed:', e);
    }
  }

  async function requestDeleteFile(displayPath) {
    if (!displayPath)
      return;
    if (!await ctx.confirmAsync('Delete ' + displayPath + '?', 'delete'))
      return;
    await deleteFile(displayPath);
  }

  async function moveFileToDir(displayPath, targetDir) {
    var srcPath = String(displayPath || '').trim();
    var dstDir = String(targetDir || '').trim();
    var name;
    var dstPath;
    var srcFsPath;
    var dstFsPath;
    var dstDirPath;
    var dstFsDirPath;

    if (!srcPath || !dstDir)
      return;
    if (!srcPath.startsWith('/') || !dstDir.startsWith('/'))
      return;

    name = srcPath.slice(srcPath.lastIndexOf('/') + 1).toLowerCase();
    if (!name || !ctx.isUserFile(name))
      return;

    if (!dstDir.endsWith('/'))
      dstDir += '/';
    dstPath = dstDir + name;
    if (dstPath === srcPath)
      return;

    srcFsPath = toFsPath(ctx.currentDir, srcPath);
    dstFsPath = toFsPath(ctx.currentDir, dstPath);
    dstDirPath = dstDir.replace(/\/$/, '');
    dstFsDirPath = toFsPath(ctx.currentDir, dstDirPath);
    if (ctx.safeStat(dstFsPath)) {
      if (!await ctx.confirmAsync('Overwrite ' + dstPath + '?', 'overwrite'))
        return;
    }

    try {
      ctx.safeMkdirTree(dstFsDirPath);
      ctx.safeUnlink(dstFsPath);
      FS.rename(srcFsPath, dstFsPath);
      syncAndRefresh();
    } catch (e) {
      ctx.showErrorMessage('Move failed', 3000);
      console.error('Move failed:', e);
    }
  }

  function cycleDir(step) {
    var dirs = getDirs();
    var index;
    var delta = step < 0 ? -1 : 1;
    if (!dirs.length)
      return;
    index = dirs.indexOf(ctx.currentDir);
    if (index < 0)
      index = 0;
    index = (index + delta + dirs.length) % dirs.length;
    ctx.currentDir = dirs[index];
    ctx.refresh();
  }

  function refresh() {
    var files;
    var dirName;
    var isCdMode;
    var serverTracks = [];
    var runtime = null;
    var localTrackNumbers = null;

    ctx.syncCdModeUI();
    ctx.syncConfigToggle();
    ctx.syncJoinCode();
    if (ctx.fileInput) ctx.fileInput.accept = ctx.getCurrentUploadSettings().accept;
    buildTabs();

    dirName = ctx.currentDir.replace(/^\/|\/$/g, '');
    ctx.dirLabel.textContent = dirName;
    ctx.list.innerHTML = '';
    closeEditor();

    if (typeof FS === 'undefined') {
      ctx.list.innerHTML = '<li style="color:#888">FS not ready</li>';
      return;
    }

    isCdMode = ctx.isCdDir(ctx.currentDir);

    if (isCdMode) {
      var cdBackupRoot = String(ctx.CD_USERFS || '').replace(/\/$/, '');

      ctx.ensureCdDirs();
      files = collectFiles(cdBackupRoot, ctx.isCdFile)
        .map(function(path) { return path.indexOf(cdBackupRoot + '/') === 0 ? (ctx.CD_DIR + path.slice(cdBackupRoot.length + 1)) : ''; })
        .filter(function(path) {
          return path.startsWith('/');
        });
      localTrackNumbers = Object.create(null);
      files.forEach(function(path) {
        var trackNumber = ctx.getCdTrackNumber(path);
        if (trackNumber > 0)
          localTrackNumbers[trackNumber] = true;
      });
      serverTracks = ctx.getCdRemoteTracks().sort();
      runtime = ctx.getCdRuntimeState();
    } else {
      files = collectFiles(ctx.USERFS, ctx.isUserFile)
        .map(function(path) { return path.indexOf(ctx.USERFS + '/') === 0 ? path.slice(ctx.USERFS.length) : ''; })
        .filter(function(path) { return path.startsWith('/'); });
      files = files.filter(function(path) { return path.indexOf(ctx.currentDir) === 0; });
    }

    files.sort();

    if (isCdMode && !files.length) {
      ctx.list.innerHTML = '<li style="color:#888">No user CD tracks</li>';
      if (!serverTracks.length) return;
    }

    files.forEach(function(displayPath) {
      var shownName = displayPath;
      var li;
      var span;
      var actions;
      var cdBtn;
      var execBtn;
      var dlBtn;
      var delBtn;

      if (displayPath.startsWith(ctx.currentDir))
        shownName = displayPath.slice(ctx.currentDir.length);

      li = document.createElement('li');
      li.setAttribute('data-path', displayPath);
      li.draggable = !isCdMode;

      span = document.createElement('span');
      span.className = 'nq-fname';
      span.textContent = shownName;
      if (!isCdMode && isCfg(displayPath))
        span.classList.add('nq-editable');

      if (isCdMode) {
        ctx.setCdTrackMeta(li, displayPath, false);
        cdBtn = ctx.createCdTrackToggleButton(displayPath, false, runtime);
        li.appendChild(cdBtn);
        ctx.applyCdTrackElementState(li, runtime);
      }
      li.appendChild(span);
      actions = document.createElement('div');
      actions.className = 'nq-file-actions';

      if (!isCdMode && isCfg(displayPath)) {
        var editBtn = document.createElement('button');
        editBtn.className = 'nq-edit';
        editBtn.innerHTML = '<svg viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\"><path d=\"M17 3a2.83 2.83 0 1 1 4 4L7 21l-4 1 1-4Z\"/><path d=\"M15 5l4 4\"/></svg>';
        editBtn.title = 'edit cfg';
        editBtn.setAttribute('aria-label', 'edit cfg');
        actions.appendChild(editBtn);
        execBtn = document.createElement('button');
        execBtn.className = 'nq-exec';
        execBtn.innerHTML = FILE_EXEC_ICON;
        execBtn.title = 'exec cfg';
        execBtn.setAttribute('aria-label', 'exec cfg');
        actions.appendChild(execBtn);
      }

      dlBtn = document.createElement('button');
      dlBtn.className = 'nq-dl';
      dlBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>';
      dlBtn.title = 'download';
      actions.appendChild(dlBtn);

      delBtn = document.createElement('button');
      delBtn.innerHTML = FILE_DELETE_ICON;
      delBtn.className = 'nq-del';
      delBtn.title = 'delete';
      actions.appendChild(delBtn);

      li.appendChild(actions);
      ctx.list.appendChild(li);
    });

    if (!isCdMode && !files.length) {
      ctx.list.innerHTML = '<li style="color:#888">No user files</li>';
      return;
    }

    if (isCdMode)
      ctx.appendCdServerTracks(serverTracks, runtime, localTrackNumbers);
  }

  Object.assign(ctx, {
    openEditor,
    closeEditor,
    execCfgFile,
    deleteFile,
    requestDeleteFile,
    moveFileToDir,
    cycleDir,
    refresh
  });
})();
