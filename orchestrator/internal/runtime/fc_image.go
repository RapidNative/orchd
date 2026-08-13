//go:build linux

package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FC image prep: turn a built docker workspace image into a warm dm-thin base
// volume that project VMs clone. The S2 spike's 500-after-restore taught the
// hard rule here: the base must come from a CLEAN export (never a crash-copied
// filesystem), and the warm state is produced by exactly one template boot.
//
// Layout under <root>/images/<name>/:
//   meta.json   {BaseDevID, WarmDevID, ...}
// Volumes: base (pristine export + init/agent), warm (base + one booted,
// bundle-warmed, cleanly shut down session). Clones snapshot the WARM volume.

type fcImageMeta struct {
	Name      string    `json:"name"`       // e.g. "fullstack-supabase@v44/mobile"
	DockerTag string    `json:"docker_tag"` // source image
	BaseDevID int       `json:"base_dev_id"`
	WarmDevID int       `json:"warm_dev_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *FirecrackerDriver) imageDir(name string) string {
	return filepath.Join(d.cfg.Root, "images", sanitizeTagFC(name))
}

func (d *FirecrackerDriver) imageMeta(name string) (*fcImageMeta, error) {
	b, err := os.ReadFile(filepath.Join(d.imageDir(name), "meta.json"))
	if err != nil {
		return nil, err
	}
	m := &fcImageMeta{}
	return m, json.Unmarshal(b, m)
}

// PrepareImage builds the base + warm volumes for a docker workspace image.
// runCmd is the workload's boot command (the image CMD's script); warmPath,
// when non-empty, is requested once against the booted template VM so its
// caches and bundle are hot inside the warm volume AND the memory snapshot.
// The template memory snapshot (tpl.state/tpl.mem in the image dir) is kept
// for a future template-restore cold tier; project creates today fresh-boot
// from the warm volume.
// PrepareImage derives the fc image name from the docker tag so the create
// path (which only has spec.Image) always finds it.
func (d *FirecrackerDriver) PrepareImage(ctx context.Context, dockerTag, runCmd, warmPath string) error {
	name := sanitizeTagFC(dockerTag)
	dir := d.imageDir(name)
	if _, err := d.imageMeta(name); err == nil {
		return fmt.Errorf("image %s already prepared (delete %s to redo)", name, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// 1) clean export -> base thin volume
	baseID, err := d.pool.nextDevID()
	if err != nil {
		return err
	}
	baseDM := "fcimg-" + sanitizeTagFC(name) + "-base"
	if err := d.pool.createThin(baseID, baseDM); err != nil {
		return err
	}
	if err := d.exportToVolume(ctx, dockerTag, "/dev/mapper/"+baseDM, runCmd); err != nil {
		return fmt.Errorf("export: %w", err)
	}

	// 2) warm volume = snapshot of base, boot once, warm, clean shutdown
	warmID, err := d.pool.nextDevID()
	if err != nil {
		return err
	}
	warmDM := "fcimg-" + sanitizeTagFC(name) + "-warm"
	if err := d.pool.snapshotOf(baseID, warmID, warmDM); err != nil {
		return err
	}
	meta := &fcImageMeta{Name: name, DockerTag: dockerTag, BaseDevID: baseID, WarmDevID: warmID, CreatedAt: time.Now()}

	tref := "tpl-" + sanitizeTagFC(name)
	tm := &vmMeta{Ref: tref, DevID: warmID, NetIdx: warmID, Image: name}
	if err := os.MkdirAll(d.vmDir(tref), 0o755); err != nil {
		return err
	}
	// point the template VM's rootfs at the warm volume
	_ = os.Remove(filepath.Join(d.vmDir(tref), "rootfs.blk"))
	if err := os.Symlink("/dev/mapper/"+warmDM, filepath.Join(d.vmDir(tref), "rootfs.blk")); err != nil {
		return err
	}
	if err := d.saveMeta(tm); err != nil {
		return err
	}
	inst, err := d.boot(ctx, tm, Spec{Ref: tref, ReadyTimeout: 5 * time.Minute}, false)
	if err != nil {
		return fmt.Errorf("template boot: %w", err)
	}
	if warmPath != "" {
		if err := httpDrain(ctx, "http://"+inst.Addr+warmPath, 5*time.Minute); err != nil {
			d.kill(tm)
			return fmt.Errorf("warm request: %w", err)
		}
	}
	// Clean shutdown WHILE RUNNING so the warm volume's filesystem is
	// consistent — a paused VM cannot process /halt, and pause-then-kill
	// captures torn cache writes that break every clone's first bundle with
	// "ctx: Unexpected end of JSON input" (learned twice: S2, then here).
	// The template memory snapshot (cold-tier restore) is deliberately NOT
	// taken in v1 — it needs its own disk+memory pair on a child volume.
	n := &vmNet{Ref: tref, Idx: tm.NetIdx}
	_, _ = httpPost(fmt.Sprintf("http://%s/halt", n.Addr(d.cfg.AgentPort)), "")
	for i := 0; i < 300 && pidAlive(tm.PID); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	d.kill(tm)
	(&vmNet{Ref: tref, Idx: tm.NetIdx}).destroy()
	_ = os.RemoveAll(d.vmDir(tref))

	b, _ := json.MarshalIndent(meta, "", " ")
	return os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644)
}

// exportToVolume writes a pristine docker-export of the image onto the block
// device, then bakes the orchd init + guest agent into it.
func (d *FirecrackerDriver) exportToVolume(ctx context.Context, dockerTag, dev, runCmd string) error {
	tmp, err := os.MkdirTemp(d.cfg.Root, "export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cid, err := shOut("docker", "create", dockerTag)
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	defer sh("docker", "rm", "-f", cid)
	tar := filepath.Join(tmp, "fs.tar")
	if err := sh("docker", "export", cid, "-o", tar); err != nil {
		return err
	}
	if err := sh("mkfs.ext4", "-q", dev); err != nil {
		return err
	}
	mnt := filepath.Join(tmp, "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := sh("mount", dev, mnt); err != nil {
		return err
	}
	defer sh("umount", mnt)
	if err := sh("tar", "xf", tar, "-C", mnt); err != nil {
		return err
	}
	for _, p := range []string{"proc", "sys", "dev", "tmp", "data", "etc"} {
		_ = os.MkdirAll(filepath.Join(mnt, p), 0o755)
	}
	if err := os.WriteFile(filepath.Join(mnt, "opt", "orchd-agent.js"), []byte(fcAgentJS), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(mnt, "etc", "resolv.conf"), []byte(fcResolvConf), 0o644); err != nil {
		return err
	}
	init := fmt.Sprintf(fcInitTemplate, runCmd)
	if err := os.WriteFile(filepath.Join(mnt, "sbin", "orchd-init"), []byte(init), 0o755); err != nil {
		return err
	}
	return sh("sync")
}

func sanitizeTagFC(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

// fcResolvConf is the guest resolver. Public resolvers rather than the host's,
// because the sandbox reaches the network through NAT and the host's own
// nameserver may be link-local or unreachable from inside.
const fcResolvConf = "nameserver 1.1.1.1\nnameserver 8.8.8.8\noptions timeout:2 attempts:2\n"

// fcInitTemplate is the guest PID 1: mounts, env, agent, then the workload.
// %s is the workload run command (a shell script string).
const fcInitTemplate = `#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev 2>/dev/null
mount -t tmpfs tmpfs /tmp
[ -f /etc/orchd.env ] && . /etc/orchd.env
export PORT=${PORT:-8080}
node /opt/orchd-agent.js > /var/log/orchd-agent.log 2>&1 &
( cd /app && %s ) > /var/log/workload.log 2>&1 &
wait -n
sync
exec sleep infinity
`

// fcAgentJS is the in-guest agent: file put/delete, exec, logs, halt.
const fcAgentJS = `const http=require('http'),fs=require('fs'),path=require('path'),cp=require('child_process');
const srv=http.createServer((req,res)=>{
  let b='';req.on('data',c=>b+=c);req.on('end',()=>{
    try{
      const u=new URL(req.url,'http://a');
      if(req.method==='PUT'&&u.pathname==='/file'){
        const{p,b64}=JSON.parse(b);fs.mkdirSync(path.dirname(p),{recursive:true});
        fs.writeFileSync(p,Buffer.from(b64,'base64'));return res.end('ok');
      }
      if(req.method==='DELETE'&&u.pathname==='/file'){
        const{p}=JSON.parse(b);fs.rmSync(p,{force:true,recursive:true});return res.end('ok');
      }
      if(req.method==='POST'&&u.pathname==='/exec'){
        const{cmd}=JSON.parse(b);
        cp.exec(cmd,{timeout:300000,maxBuffer:8*1024*1024},(e,so,se)=>{
          res.end(JSON.stringify({code:e?e.code??1:0,out:String(so).slice(-4000),err:String(se).slice(-4000)}));
        });return;
      }
      if(u.pathname==='/logs'){
        const t=parseInt(u.searchParams.get('tail')||'200');
        let out='';try{out=fs.readFileSync('/var/log/workload.log','utf8').split('\n').slice(-t).join('\n')}catch{}
        return res.end(out);
      }
      if(u.pathname==='/halt'){res.end('halting');cp.exec('sync');setTimeout(()=>cp.exec('poweroff -f'),300);return;}
      if(u.pathname==='/health')return res.end('ok');
      res.statusCode=404;res.end('nf');
    }catch(e){res.statusCode=500;res.end(String(e));}
  });
});
srv.listen(9000,'0.0.0.0');
`

// ---- small shared helpers ----

func encodeB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// httpDrain GETs a URL and consumes the whole body (a bundle warm must never
// be aborted mid-flight — that cancels the dev server's work).
func httpDrain(ctx context.Context, url string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := httpNewRequest(cctx, "GET", url)
	if err != nil {
		return err
	}
	res, err := httpDo(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = ioCopyDiscard(res.Body)
	if res.StatusCode >= 400 {
		return fmt.Errorf("warm %s: %s", url, res.Status)
	}
	return nil
}

// Thin wrappers so the helpers above read clearly without extra imports at
// every call site.
func httpNewRequest(ctx context.Context, method, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, nil)
}
func httpDo(req *http.Request) (*http.Response, error) { return http.DefaultClient.Do(req) }
func httpPost(url, body string) (*http.Response, error) {
	return http.Post(url, "application/json", strings.NewReader(body))
}
func ioCopyDiscard(r io.Reader) (int64, error) { return io.Copy(io.Discard, r) }
