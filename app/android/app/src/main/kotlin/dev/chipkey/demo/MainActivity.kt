package dev.chipkey.demo

import io.flutter.embedding.android.FlutterFragmentActivity
import io.flutter.embedding.engine.FlutterEngine

// BiometricPrompt needs a FragmentActivity, so this extends
// FlutterFragmentActivity instead of the template's FlutterActivity.
class MainActivity : FlutterFragmentActivity() {
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        HardwareKey(this, flutterEngine.dartExecutor.binaryMessenger)
    }
}
