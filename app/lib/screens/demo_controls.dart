import 'package:flutter/material.dart';

import '../backend.dart';
import '../controller.dart';
import '../theme.dart';

/// Stage controls, kept out of the way behind an icon: which character this
/// phone plays, the attack switch, a reset, and the backend address.
Future<void> showDemoControls(
  BuildContext context,
  DemoController c,
  HttpBackend? backend,
) {
  return showModalBottomSheet<void>(
    context: context,
    backgroundColor: Colors.white,
    showDragHandle: true,
    // Size the sheet to its content, and let it scroll on a short screen.
    isScrollControlled: true,
    builder: (sheet) => ListenableBuilder(
      listenable: c,
      builder: (context, _) => SafeArea(
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const ListTile(
                dense: true,
                title: Text(
                  'Demo controls',
                  style: TextStyle(fontSize: 18, fontWeight: FontWeight.w600),
                ),
              ),
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
                child: SizedBox(
                  width: double.infinity,
                  child: SegmentedButton<Role>(
                    key: const Key('role'),
                    showSelectedIcon: false,
                    segments: const [
                      ButtonSegment(
                        value: Role.owner,
                        icon: Icon(Icons.person_outline),
                        label: Text("Meera's phone"),
                      ),
                      ButtonSegment(
                        value: Role.thief,
                        icon: Icon(Icons.forward_to_inbox),
                        label: Text("Thief's phone"),
                      ),
                    ],
                    selected: {c.role},
                    onSelectionChanged: (pick) => c.setRole(pick.first),
                  ),
                ),
              ),
              Padding(
                padding: const EdgeInsets.fromLTRB(18, 0, 18, 4),
                child: Text(
                  c.role == Role.owner
                      ? 'Her SIM is in this phone: SMS arrive here.'
                      : 'This phone receives the SMS her phone forwards.',
                  style: const TextStyle(color: Brand.grey, fontSize: 12),
                ),
              ),
              SwitchListTile(
                key: const Key('tamper'),
                activeThumbColor: Brand.red,
                secondary: const Icon(
                  Icons.bug_report_outlined,
                  color: Brand.red,
                ),
                title: const Text('Attack mode'),
                subtitle: const Text('Rewrite the payee after the chip signs'),
                value: c.tamper,
                onChanged: c.setTamper,
              ),
              ListTile(
                leading: const Icon(Icons.memory_outlined),
                title: Text(
                  c.status.enrolled
                      ? 'Device key in ${(c.tier ?? 'hardware').toUpperCase()}'
                      : 'No device key',
                ),
                subtitle: Text(
                  c.status.enrolled
                      ? 'Key ID ${c.verdict?.keyId ?? c.status.keyId ?? 'unknown'}'
                      : 'Log in to bind this phone',
                ),
              ),
              ListTile(
                key: const Key('reset'),
                leading: const Icon(Icons.delete_outline),
                title: const Text('Reset device binding'),
                subtitle: const Text('Delete the key and log out'),
                onTap: c.busy
                    ? null
                    : () async {
                        await c.reset();
                        if (sheet.mounted) {
                          Navigator.of(
                            sheet,
                          ).popUntil((route) => route.isFirst);
                        }
                      },
              ),
              if (backend != null)
                ListTile(
                  key: const Key('backendAddress'),
                  leading: const Icon(Icons.dns_outlined),
                  title: const Text('Backend address'),
                  subtitle: Text(backend.baseUrl),
                  onTap: () async {
                    final field = TextEditingController(text: backend.baseUrl);
                    final url = await showDialog<String>(
                      context: sheet,
                      builder: (dialog) => AlertDialog(
                        title: const Text('Backend address'),
                        content: TextField(
                          key: const Key('backendField'),
                          controller: field,
                          keyboardType: TextInputType.url,
                          autocorrect: false,
                        ),
                        actions: [
                          TextButton(
                            key: const Key('backendUsb'),
                            onPressed: () =>
                                Navigator.pop(dialog, defaultBackendUrl),
                            child: const Text('USB default'),
                          ),
                          TextButton(
                            onPressed: () => Navigator.pop(dialog),
                            child: const Text('Cancel'),
                          ),
                          FilledButton(
                            key: const Key('backendSave'),
                            onPressed: () =>
                                Navigator.pop(dialog, field.text.trim()),
                            child: const Text('Save'),
                          ),
                        ],
                      ),
                    );
                    if (url != null &&
                        url.isNotEmpty &&
                        url != backend.baseUrl) {
                      // Saved for the next launch. The session belonged to the
                      // old verifier, so the app returns to the login screen.
                      backend.setBaseUrl(url);
                      c.logout();
                      if (sheet.mounted) {
                        Navigator.of(sheet).popUntil((route) => route.isFirst);
                      }
                    }
                  },
                ),
              const SizedBox(height: 8),
            ],
          ),
        ),
      ),
    ),
  );
}
