import gi

gi.require_version("Gtk", "3.0")
from gi.repository import Gtk

window = Gtk.Window(title="OpticDeck benchmark · GTK")
window.set_default_size(640, 420)
window.set_border_width(28)
window.connect("destroy", Gtk.main_quit)

layout = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=14)
window.add(layout)
heading = Gtk.Label(label="GTK benchmark", xalign=0)
layout.pack_start(heading, False, False, 0)
project = Gtk.Entry(text="Original project")
project.get_accessible().set_name("Project name")
layout.pack_start(project, False, False, 0)
include = Gtk.CheckButton(label="Include draft")
layout.pack_start(include, False, False, 0)
apply = Gtk.Button(label="Apply changes")
layout.pack_start(apply, False, False, 0)
result = Gtk.Label(label="Pending", xalign=0)
layout.pack_start(result, False, False, 0)


def apply_changes(_button):
    suffix = "draft included" if include.get_active() else "draft excluded"
    result.set_text(f"Saved: {project.get_text()} ({suffix})")


apply.connect("clicked", apply_changes)
window.show_all()
Gtk.main()
