import 'package:flutter/services.dart';

/// Small app settings that should survive a restart, such as the backend
/// address. They are stored on the phone and are not secrets.
abstract class SettingsStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
}

class MethodChannelSettings implements SettingsStore {
  static const _channel = MethodChannel('chipkey');

  @override
  Future<String?> read(String key) async {
    try {
      return await _channel.invokeMethod<String>('getSetting', {'key': key});
    } on PlatformException {
      return null;
    } on MissingPluginException {
      return null;
    }
  }

  @override
  Future<void> write(String key, String value) async {
    try {
      await _channel.invokeMethod<void>('setSetting', {
        'key': key,
        'value': value,
      });
    } on PlatformException {
      // Losing a saved address only means typing it again.
    } on MissingPluginException {
      // Same.
    }
  }
}
