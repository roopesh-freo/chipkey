import 'package:flutter/services.dart';

/// What the phone knows about its own hardware key.
class ChipStatus {
  const ChipStatus({
    required this.enrolled,
    required this.biometrics,
    this.tier,
    this.keyId,
  });

  final bool enrolled;

  /// "ready", "none_enrolled", "no_hardware" or "unavailable".
  final String biometrics;

  /// Where Android says the key lives. The backend decides for itself from
  /// the attestation and never takes this value on trust.
  final String? tier;
  final String? keyId;
}

class Enrolment {
  const Enrolment({
    required this.chain,
    required this.tier,
    required this.keyId,
    required this.fellBackFromStrongBox,
  });

  /// DER certificates, leaf first, root last.
  final List<Uint8List> chain;
  final String tier;
  final String keyId;
  final bool fellBackFromStrongBox;
}

class ChipException implements Exception {
  const ChipException(this.code, this.message);
  final String code;
  final String message;

  @override
  String toString() => '$code: $message';
}

/// The whole Dart surface of the hardware key. Notice what is missing: there
/// is no way to read the private key, here or on the native side.
abstract class KeyChip {
  Future<String> deviceId();

  /// A human name for this phone, shown on the other phone and the dashboard.
  Future<String> deviceName();
  Future<ChipStatus> status();

  /// Creates the key inside secure hardware and returns its attestation.
  Future<Enrolment> enrol(Uint8List serverChallenge);

  /// Shows the biometric prompt, then the chip signs [payload].
  Future<Uint8List> sign(
    Uint8List payload, {
    required String title,
    String subtitle,
  });

  Future<void> delete();
}

class MethodChannelKeyChip implements KeyChip {
  static const _channel = MethodChannel('chipkey');

  Future<T> _guard<T>(Future<T> Function() body) async {
    try {
      return await body();
    } on PlatformException catch (e) {
      throw ChipException(e.code, e.message ?? '');
    }
  }

  @override
  Future<String> deviceId() =>
      _guard(() async => (await _channel.invokeMethod<String>('deviceId'))!);

  @override
  Future<String> deviceName() => _guard(
    () async =>
        (await _channel.invokeMethod<String>('deviceName')) ?? 'Android phone',
  );

  @override
  Future<ChipStatus> status() => _guard(() async {
    final m = (await _channel.invokeMapMethod<String, Object?>('status'))!;
    return ChipStatus(
      enrolled: m['enrolled'] as bool,
      biometrics: m['biometrics'] as String,
      tier: m['tier'] as String?,
      keyId: m['keyId'] as String?,
    );
  });

  @override
  Future<Enrolment> enrol(Uint8List serverChallenge) => _guard(() async {
    final m = (await _channel.invokeMapMethod<String, Object?>('enrol', {
      'challenge': serverChallenge,
    }))!;
    return Enrolment(
      chain: (m['chain'] as List).cast<Uint8List>(),
      tier: m['tier'] as String,
      keyId: m['keyId'] as String,
      fellBackFromStrongBox: m['fellBackFromStrongBox'] as bool,
    );
  });

  @override
  Future<Uint8List> sign(
    Uint8List payload, {
    required String title,
    String subtitle = '',
  }) => _guard(
    () async => (await _channel.invokeMethod<Uint8List>('sign', {
      'payload': payload,
      'title': title,
      'subtitle': subtitle,
    }))!,
  );

  @override
  Future<void> delete() => _guard(() => _channel.invokeMethod<void>('delete'));
}
