import gi,sys
gi.require_version('Gtk','3.0')
gi.require_version('WebKit2','4.1')
from gi.repository import Gtk,WebKit2,GLib
window=Gtk.Window(title='WebKit exit baseline (PyGObject)')
window.set_default_size(800,600)
context=WebKit2.WebContext.new_ephemeral()
view=WebKit2.WebView.new_with_context(context)
view.get_settings().set_enable_developer_extras(True)
window.add(view)
window.show_all()
view.load_html('<html><body><h1>WebKit exit baseline</h1></body></html>','https://example.invalid/')
if '--inspector' in sys.argv: view.get_inspector().show()
def close():
 print('baseline closing',flush=True)
 view.stop_loading()
 window.destroy()
 Gtk.main_quit()
 return False
def dock():
 inspector=view.get_inspector()
 print('can attach',inspector.get_can_attach(),flush=True)
 inspector.attach()
 print('attached',inspector.is_attached(),flush=True)
 window.resize(850,640)
 return False
if '--attach' in sys.argv: GLib.timeout_add(2000,dock)
GLib.timeout_add(8000,close)
Gtk.main()
print('baseline closed',flush=True)
